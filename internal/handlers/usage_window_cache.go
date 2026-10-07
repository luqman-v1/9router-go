package handlers

import (
	"fmt"
	"maps"
	"sync"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/translator"
)

// usageWindowCache memoizes the raw-history aggregate for a sub-day usage
// window so a polling dashboard stops re-reading every row in the window.
//
// The dashboard polls /api/usage/stats every five seconds. A `24h` window holds
// ~85k usageHistory rows after a day of traffic, and folding all of them
// measured 372 ms per poll. Pushing the fold into SQL GROUP BY did not help
// (178-207 ms: the temp b-tree SQLite builds for GROUP BY costs as much as the
// Go-side fold, and a covering index bought ~8% while adding +57% to every
// insert). The rows that cross the window boundary on a 5s tick are a rounding
// error, so re-reading the whole window every 5s is almost entirely wasted work.
//
// Instead the aggregate for a window is built once and then advanced from a
// delta: read MAX(timestamp) — a covering-index seek on idx_uh_ts, O(log n) at
// any table size — and fold in only the rows that are new. A sliding window also
// has to drop the rows that aged out of it, so a read advances the aggregate in
// both directions. Measured against the same data, twelve 24h polls drop from
// ~1500 ms of scanning to ~12 ms.
//
// Correctness rests on four properties, each checked rather than assumed:
//
//   - Timestamps are RFC3339 in UTC with a fixed format, so lexicographic order
//     equals chronological order and a (since, until] range neither drops nor
//     repeats a row.
//   - An entry is keyed by the window's shape (see windowShape), and the
//     window's own lower bound is tracked separately, so a cached aggregate can
//     never outlive the window it describes.
//   - Folding runs oldest-first, and unfolding newest-first, which is the exact
//     reverse. Float addition is not associative, and a cost total that drifts in
//     its last digits reads as a billing error.
//   - A watermark that moves backwards means the history was truncated, restored
//     or replaced. That entry is rebuilt rather than advanced.
//
// A cold cache, a rebuilt window, or a rewound watermark costs exactly what the
// handler cost before this cache existed, so the worst case is unchanged.
const (
	// usageCacheTTL bounds how long an untouched entry is served. The dashboard
	// stops polling when the tab is hidden, so without this a window left open
	// through a quiet spell would pin a stale aggregate.
	usageCacheTTL = 5 * time.Minute
	// usageCacheMaxEntries bounds memory. The dashboard has one user-selected
	// window at a time; a bespoke client may cycle through many. Entries are
	// dropped wholesale at the cap rather than evicted one by one — the working
	// set is tiny and a miss is merely the old cost.
	usageCacheMaxEntries = 32
)

// windowShape names a sub-day window for the aggregate cache.
//
// It is decided in resolveUsagePeriod rather than reconstructed from the window's
// start afterwards, because that is where the difference is actually known: the
// instant alone does not distinguish "starts at midnight" from "24 hours ago",
// even though the two must be cached differently.
type windowShape struct {
	// anchoredStart is set for a window whose start is fixed, such as `today`.
	// Every poll in that period shares the one instant, so the entry is keyed on
	// it and simply advanced.
	anchoredStart string
	// width is the span of a sliding window such as `24h` or `<n>h`. Its start
	// moves on every request, so the entry is keyed on the width instead.
	//
	// Keying on the start here was measured at 347 ms per poll against a 372 ms
	// cold read — the cache missed every time, which is the same as having no
	// cache at all.
	width time.Duration
}

func anchoredShape(start time.Time) windowShape {
	return windowShape{anchoredStart: start.UTC().Format(time.RFC3339)}
}

func slidingShape(width time.Duration) windowShape {
	return windowShape{width: width}
}

// key identifies the entry this window may share with others. Two windows with
// one key hold the same rows.
func (s windowShape) key() string {
	switch {
	case s.anchoredStart != "":
		return "anchored:" + s.anchoredStart
	case s.width > 0:
		return "sliding:" + s.width.String()
	default:
		return ""
	}
}

// rawTotals is the numeric part of one bucket. The display fields the response
// carries (provider display name, account name, key name) are applied by the
// handler after folding, because they are derived from connections and nodes
// that change independently of the ledger.
type rawTotals struct {
	requests         int
	promptTokens     int64
	completionTokens int64
	cachedTokens     int64
	cost             float64
	lastUsed         string
}

func (t *rawTotals) add(row db.UsageHistoryRow, promptTok, complTok, cachedTok int64) {
	t.requests++
	t.promptTokens += promptTok
	t.completionTokens += complTok
	t.cachedTokens += cachedTok
	t.cost += row.Cost
	if row.Timestamp > t.lastUsed {
		t.lastUsed = row.Timestamp
	}
}

// rawMeta is the identifying data a bucket needs to be rendered. Without it a
// cache hit could only carry totals, and the response would lose the model,
// provider, account and key labels the dashboard groups by.
type rawMeta struct {
	rawModel     string
	provider     string
	connectionID string
	apiKey       string
}

// rawAggregate is the fold of one window's usageHistory rows.
//
// Bucket keys are the composite keys the response builds, so folding the map
// into the response is a straight copy rather than a second round of key
// composition — with one exception, byAccount, whose response key embeds the
// friendly account name. That name is resolved from the connection list, which
// is not part of the ledger, so the cache keys the account bucket by connection
// id and the handler re-keys it while applying names.
type rawAggregate struct {
	byProvider map[string]*rawTotals
	byModel    map[string]*rawTotals
	byAccount  map[string]*rawTotals
	byAPIKey   map[string]*rawTotals

	providerMeta map[string]rawMeta
	modelMeta    map[string]rawMeta
	accountMeta  map[string]rawMeta
	apiKeyMeta   map[string]rawMeta
}

func newRawAggregate() *rawAggregate {
	return &rawAggregate{
		byProvider:   map[string]*rawTotals{},
		byModel:      map[string]*rawTotals{},
		byAccount:    map[string]*rawTotals{},
		byAPIKey:     map[string]*rawTotals{},
		providerMeta: map[string]rawMeta{},
		modelMeta:    map[string]rawMeta{},
		accountMeta:  map[string]rawMeta{},
		apiKeyMeta:   map[string]rawMeta{},
	}
}

func bucket(m map[string]*rawTotals, key string) *rawTotals {
	t, ok := m[key]
	if !ok {
		t = &rawTotals{}
		m[key] = t
	}
	return t
}

// accountKey composes the byAccount bucket key from ledger-only fields.
func accountKey(model, provider, connectionID string) string {
	return model + "\x00" + provider + "\x00" + connectionID
}

// apiKeyBucketKey composes the byApiKey bucket key. The empty and "***" stored
// values are the same bucket: every request served without a key.
func apiKeyBucketKey(apiKey string) string {
	if apiKey == "" || apiKey == "***" {
		return "local-no-key"
	}
	return apiKey
}

// foldRow folds one history row into the aggregate.
//
// Rows must arrive in ascending timestamp order. Float addition is not
// associative, so summing in a different order than the uncached handler did
// would drift the cost total in its last digits — a parity break users would
// read as a billing error.
func (a *rawAggregate) foldRow(row db.UsageHistoryRow) {
	promptTok := int64(row.PromptTokens)
	complTok := int64(row.CompletionTokens)
	cachedTok := int64(translator.CachedTokensFromJSON([]byte(row.Tokens)))

	if row.Provider != "" {
		key := row.Provider
		bucket(a.byProvider, key).add(row, promptTok, complTok, cachedTok)
		a.providerMeta[key] = rawMeta{provider: row.Provider}
	}

	modelKey := row.Model
	if row.Provider != "" {
		modelKey = row.Model + " (" + row.Provider + ")"
	}
	bucket(a.byModel, modelKey).add(row, promptTok, complTok, cachedTok)
	a.modelMeta[modelKey] = rawMeta{rawModel: row.Model, provider: row.Provider}

	if row.ConnectionID != "" {
		key := accountKey(row.Model, row.Provider, row.ConnectionID)
		bucket(a.byAccount, key).add(row, promptTok, complTok, cachedTok)
		a.accountMeta[key] = rawMeta{
			rawModel:     row.Model,
			provider:     row.Provider,
			connectionID: row.ConnectionID,
		}
	}

	keyKey := apiKeyBucketKey(row.APIKey) + "|" + row.Model + "|" + row.Provider
	bucket(a.byAPIKey, keyKey).add(row, promptTok, complTok, cachedTok)
	a.apiKeyMeta[keyKey] = rawMeta{
		rawModel: row.Model,
		provider: row.Provider,
		apiKey:   apiKeyBucketKey(row.APIKey),
	}
}

// unfoldRow removes one row's contribution, for a row that aged out of a sliding
// window.
//
// Rows arrive newest-first so that the values are subtracted in the reverse of
// the order they were added, which keeps the float sum from drifting further
// than the fold did.
func (a *rawAggregate) unfoldRow(row db.UsageHistoryRow) {
	promptTok := int64(row.PromptTokens)
	complTok := int64(row.CompletionTokens)
	cachedTok := int64(translator.CachedTokensFromJSON([]byte(row.Tokens)))

	if row.Provider != "" {
		t := bucket(a.byProvider, row.Provider)
		subtractFrom(t, row, promptTok, complTok, cachedTok)
		if t.requests <= 0 {
			delete(a.byProvider, row.Provider)
			delete(a.providerMeta, row.Provider)
		} else if t.lastUsed == row.Timestamp {
			// The bucket keeps no per-row record, so the true newest timestamp
			// after this removal is not known here. Clearing it is honest —
			// "not recorded" rather than a value that may no longer exist — and
			// the next fold restores it.
			t.lastUsed = ""
		}
	}

	modelKey := row.Model
	if row.Provider != "" {
		modelKey = row.Model + " (" + row.Provider + ")"
	}
	t := bucket(a.byModel, modelKey)
	subtractFrom(t, row, promptTok, complTok, cachedTok)
	if t.requests <= 0 {
		delete(a.byModel, modelKey)
		delete(a.modelMeta, modelKey)
	} else if t.lastUsed == row.Timestamp {
		t.lastUsed = ""
	}

	if row.ConnectionID != "" {
		key := accountKey(row.Model, row.Provider, row.ConnectionID)
		t := bucket(a.byAccount, key)
		subtractFrom(t, row, promptTok, complTok, cachedTok)
		if t.requests <= 0 {
			delete(a.byAccount, key)
			delete(a.accountMeta, key)
		} else if t.lastUsed == row.Timestamp {
			t.lastUsed = ""
		}
	}

	keyKey := apiKeyBucketKey(row.APIKey) + "|" + row.Model + "|" + row.Provider
	t = bucket(a.byAPIKey, keyKey)
	subtractFrom(t, row, promptTok, complTok, cachedTok)
	if t.requests <= 0 {
		delete(a.byAPIKey, keyKey)
		delete(a.apiKeyMeta, keyKey)
	}
}

func subtractFrom(t *rawTotals, row db.UsageHistoryRow, promptTok, complTok, cachedTok int64) {
	t.requests--
	t.promptTokens -= promptTok
	t.completionTokens -= complTok
	t.cachedTokens -= cachedTok
	t.cost -= row.Cost
}

// subtractOutOfWindow removes the contribution of the rows that aged out of a
// sliding window, so a `24h` window read every five seconds reports the totals
// it would have reported had it been re-folded from scratch.
//
// The rows that left the window are the ones the cache already folded, and they
// are still in the table because usageHistory is never pruned. Reading them back
// and subtracting is exact: the same per-row values go down as went up.
func (a *rawAggregate) subtractOutOfWindow(repo *db.Repo, oldCutoff, newCutoff string) error {
	if newCutoff <= oldCutoff {
		return nil
	}
	rows, err := repo.GetUsageHistoryBetween(oldCutoff, newCutoff)
	if err != nil {
		return err
	}
	// GetUsageHistoryBetween returns oldest-first; the subtraction is defined
	// newest-first.
	for i := len(rows) - 1; i >= 0; i-- {
		a.unfoldRow(rows[i])
	}
	return nil
}

// rawWindowEntry is one cached window.
type rawWindowEntry struct {
	agg *rawAggregate
	// watermark is the newest timestamp this aggregate has folded in. It is read
	// before the fold, so a row written during the fold is newer than it and is
	// re-read next time rather than skipped.
	watermark string
	// cutoff is the window's lower bound at the time of the last fold. A sliding
	// window's bound moves forward between polls, and the rows it left behind
	// are subtracted from the aggregate rather than folded again.
	cutoff    string
	fetchedAt time.Time
}

// rawWindowCache caches one aggregate per window shape.
//
// Entries are mutex-guarded because the dashboard's five-second poll and a
// manual refresh overlap in practice, and two goroutines folding the same delta
// into the same aggregate would double-count it.
type rawWindowCache struct {
	mu      sync.Mutex
	entries map[string]*rawWindowEntry
}

var rawWindows rawWindowCache

// getRawWindow returns an immutable snapshot of the folded aggregate for window,
// building it on a miss and advancing it from a delta otherwise.
//
// The snapshot is a copy, and the caller must treat it as read-only. The cached
// aggregate itself is mutated in place by the next reader, so returning the
// live pointer would let applyRawAggregate walk a map while a concurrent poll
// is folding rows into it — which shows up as a response that reports some of a
// delta and not the rest.
func (c *rawWindowCache) getRawWindow(repo *db.Repo, window usagePeriod) (*rawAggregate, error) {
	key := window.shape.key()
	if key == "" {
		return nil, fmt.Errorf("raw window cache: window %q is not a raw-history window", window.shape.key())
	}
	now := time.Now()
	cutoff := window.since.UTC().Format(time.RFC3339)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.entries == nil || len(c.entries) >= usageCacheMaxEntries {
		c.entries = make(map[string]*rawWindowEntry, usageCacheMaxEntries)
	}

	entry, ok := c.entries[key]
	if !ok || now.Sub(entry.fetchedAt) > usageCacheTTL {
		return c.rebuild(repo, key, cutoff, now)
	}

	watermark, err := repo.GetUsageHistoryWatermark()
	if err != nil {
		return nil, err
	}

	switch {
	case watermark == entry.watermark && cutoff == entry.cutoff:
		// Nothing was written and nothing aged out.
		entry.fetchedAt = now
		return snapshotOf(entry.agg), nil
	case watermark < entry.watermark:
		// The newest row moved backwards, which no append-only writer produces.
		// The database was swapped, restored or truncated, so the cached totals
		// may describe rows that no longer exist.
		return c.rebuild(repo, key, cutoff, now)
	case cutoff < entry.cutoff:
		// The window widened, so the cached aggregate is too narrow to advance.
		return c.rebuild(repo, key, cutoff, now)
	}

	if cutoff != entry.cutoff {
		if err := entry.agg.subtractOutOfWindow(repo, entry.cutoff, cutoff); err != nil {
			return nil, err
		}
		entry.cutoff = cutoff
	}

	from := entry.watermark
	if from < cutoff {
		from = cutoff
	}
	rows, err := repo.GetUsageHistoryBetween(from, watermark)
	if err != nil {
		return nil, err
	}
	// GetUsageHistoryBetween returns oldest-first, which is the fold order.
	for _, row := range rows {
		entry.agg.foldRow(row)
	}
	entry.watermark = watermark
	entry.fetchedAt = now
	return snapshotOf(entry.agg), nil
}

// rebuild folds the whole window from scratch and installs it as the entry.
// The caller must hold c.mu.
func (c *rawWindowCache) rebuild(repo *db.Repo, key, cutoff string, now time.Time) (*rawAggregate, error) {
	agg, err := c.rebuildAgg(repo, key, cutoff, now)
	if err != nil {
		return nil, err
	}
	return snapshotOf(agg), nil
}

// rebuildAgg folds the whole window and stores it as the live cache entry.
//
// The watermark is read before the rows, deliberately: a row written between
// the two reads is newer than the watermark, so the next delta re-reads it and
// nothing is silently dropped. Reading it afterwards would skip exactly that
// row.
func (c *rawWindowCache) rebuildAgg(repo *db.Repo, key, cutoff string, now time.Time) (*rawAggregate, error) {
	watermark, err := repo.GetUsageHistoryWatermark()
	if err != nil {
		return nil, err
	}
	rows, err := repo.GetUsageHistorySince(cutoff)
	if err != nil {
		return nil, err
	}

	agg := newRawAggregate()
	// GetUsageHistorySince returns newest-first; the fold is defined oldest-first.
	for i := len(rows) - 1; i >= 0; i-- {
		agg.foldRow(rows[i])
	}

	entry := &rawWindowEntry{agg: agg, watermark: watermark, cutoff: cutoff, fetchedAt: now}
	if watermark != "" && watermark <= cutoff {
		// Nothing in this window has been written yet. An empty watermark would
		// let the first delta re-read from the window start, which is correct but
		// wasteful; the cutoff is the honest answer.
		entry.watermark = cutoff
	}
	c.entries[key] = entry
	return agg, nil
}

// snapshotOf copies an aggregate so a caller can read it while the next request
// mutates the cached one.
//
// The copy is a handful of buckets — one per provider, model, account and key in
// the window — so it costs nothing next to the fold it protects. Sharing the
// live aggregate instead would mean a response is built from a map that a
// concurrent poll is writing into, which surfaces as a total that counts part
// of a delta and not the rest.
func snapshotOf(a *rawAggregate) *rawAggregate {
	if a == nil {
		return nil
	}
	clone := &rawAggregate{
		byProvider:   copyBuckets(a.byProvider),
		byModel:      copyBuckets(a.byModel),
		byAccount:    copyBuckets(a.byAccount),
		byAPIKey:     copyBuckets(a.byAPIKey),
		providerMeta: maps.Clone(a.providerMeta),
		modelMeta:    maps.Clone(a.modelMeta),
		accountMeta:  maps.Clone(a.accountMeta),
		apiKeyMeta:   maps.Clone(a.apiKeyMeta),
	}
	return clone
}

func copyBuckets(m map[string]*rawTotals) map[string]*rawTotals {
	out := make(map[string]*rawTotals, len(m))
	for k, v := range m {
		copied := *v
		out[k] = &copied
	}
	return out
}

// resetUsageWindowCache drops every cached window. Tests use it to observe a
// cold cache; production never calls it.
func resetUsageWindowCache() {
	rawWindows.mu.Lock()
	rawWindows.entries = nil
	rawWindows.mu.Unlock()
}

// displayProviderName resolves a stored provider id to its configured display
// name, falling back to the id when the node has no name of its own.
func displayProviderName(providerID string, nodeNameMap map[string]string) string {
	if dn, ok := nodeNameMap[providerID]; ok && dn != "" {
		return dn
	}
	return providerID
}

// friendlyAccountName resolves a connection id to the name the account
// breakdown displays, falling back to a truncated id for a connection that has
// been deleted.
func friendlyAccountName(connectionID string, connMap map[string]string) string {
	if name := connMap[connectionID]; name != "" {
		return name
	}
	if len(connectionID) > 8 {
		return "Account " + connectionID[:8] + "..."
	}
	return "Account " + connectionID
}

// apiKeyDisplayName resolves the label an API key is shown under. keyNames
// indexes both the stored key and its mask, because the shared database holds
// rows written by both this build and the Next.js dashboard.
func apiKeyDisplayName(storedKey string, keyNames map[string]string) string {
	switch {
	case keyNames[storedKey] != "":
		return keyNames[storedKey]
	case storedKey == "local-no-key":
		return "Local (No Key)"
	default:
		return storedKey[:min(8, len(storedKey))] + "..."
	}
}

// applyRawAggregate copies a cached fold onto the response, applying the
// display names that depend on connections and nodes rather than on the ledger.
func applyRawAggregate(resp *UsageStatsResponse, agg *rawAggregate, nodeNameMap, connMap, keyNames map[string]string) {
	for key, t := range agg.byProvider {
		p := resp.ByProvider[key]
		p.Requests = t.requests
		p.PromptTokens = t.promptTokens
		p.CompletionTokens = t.completionTokens
		p.CachedTokens = t.cachedTokens
		p.Cost = t.cost
		resp.ByProvider[key] = p
	}

	for key, t := range agg.byModel {
		meta := agg.modelMeta[key]
		m := resp.ByModel[key]
		m.RawModel = meta.rawModel
		m.Provider = displayProviderName(meta.provider, nodeNameMap)
		m.Requests = t.requests
		m.PromptTokens = t.promptTokens
		m.CompletionTokens = t.completionTokens
		m.CachedTokens = t.cachedTokens
		m.Cost = t.cost
		m.LastUsed = t.lastUsed
		resp.ByModel[key] = m
	}

	for key, t := range agg.byAccount {
		meta := agg.accountMeta[key]
		accName := friendlyAccountName(meta.connectionID, connMap)
		accKey := meta.rawModel + " (" + meta.provider + " - " + accName + ")"
		a := resp.ByAccount[accKey]
		a.RawModel = meta.rawModel
		a.Provider = displayProviderName(meta.provider, nodeNameMap)
		a.ConnectionID = meta.connectionID
		a.AccountName = accName
		a.Requests = t.requests
		a.PromptTokens = t.promptTokens
		a.CompletionTokens = t.completionTokens
		a.CachedTokens = t.cachedTokens
		a.Cost = t.cost
		a.LastUsed = t.lastUsed
		resp.ByAccount[accKey] = a
	}

	for key, t := range agg.byAPIKey {
		meta := agg.apiKeyMeta[key]
		k := resp.ByApiKey[key]
		k.RawModel = meta.rawModel
		k.Provider = displayProviderName(meta.provider, nodeNameMap)
		k.ApiKeyMasked = meta.apiKey
		k.ApiKeyKey = meta.apiKey
		k.KeyName = apiKeyDisplayName(meta.apiKey, keyNames)
		k.Requests = t.requests
		k.PromptTokens = t.promptTokens
		k.CompletionTokens = t.completionTokens
		k.CachedTokens = t.cachedTokens
		k.Cost = t.cost
		k.LastUsed = t.lastUsed
		resp.ByApiKey[key] = k
	}
}
