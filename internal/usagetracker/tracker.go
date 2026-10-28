package usagetracker

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
	"9router/proxy/internal/translator"
)

const (
	pendingTimeout = 60 * time.Second
	ringCap        = 50
)

// ActiveRequest matches the Next.js dashboard activeRequests item shape.
type ActiveRequest struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Account  string `json:"account"`
	Count    int    `json:"count"`
	// RequestedModel is the name the client asked for, and ComboName is set
	// only when a combo served the request. A combo request reports the member
	// that serves it in Model/Provider, so the dashboard can draw
	// 9router-go -> combo -> member instead of showing the member as if the
	// caller had asked for it by name. ComboName is the explicit signal: the
	// map must not have to guess which names are combos.
	RequestedModel string `json:"requestedModel,omitempty"`
	ComboName      string `json:"comboName,omitempty"`
}

// RecentRequest matches the Next.js dashboard recentRequests item shape.
type RecentRequest struct {
	Timestamp        string `json:"timestamp"`
	Model            string `json:"model"`
	Provider         string `json:"provider"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	CachedTokens     int    `json:"cachedTokens"`
	Status           string `json:"status"`
	RequestedModel   string `json:"requestedModel,omitempty"`
	ComboName        string `json:"comboName,omitempty"`
	// LatencyMs is how long the request took, recorded for completed requests
	// only: a refused member has a rejection time, not a latency, and the map
	// must not present one as the other.
	LatencyMs int64 `json:"latencyMs,omitempty"`
}

// StreamPayload represents the payload sent over SSE on /api/usage/stream.
type StreamPayload struct {
	ActiveRequests []ActiveRequest `json:"activeRequests"`
	RecentRequests []RecentRequest `json:"recentRequests"`
	ErrorProvider  string          `json:"errorProvider"`
	Pending        PendingState    `json:"pending"`
}

// PendingState represents in-flight counts by model and account.
type PendingState struct {
	ByModel   map[string]int            `json:"byModel"`
	ByAccount map[string]map[string]int `json:"byAccount"`
}

// Tracker tracks in-flight requests, recent completions, and broadcasts SSE updates.
type Tracker struct {
	mu        sync.RWMutex
	byModel   map[string]int
	byAccount map[string]map[string]int
	// requestedByKey maps the same "model (provider)" key the counters use to
	// the names behind the request. The counters cannot tell two requests for
	// the same member apart, so neither can this: if two combos are in flight
	// on one member at once, the later start names the row and the row goes
	// back to unnamed once the count drains. Completed requests do not rely on
	// it — each carries its own names on the ring entry.
	requestedByKey    map[string]pendingNames
	lastErrorProvider string
	lastErrorTs       int64
	recentRing        []RecentRequest
	ringInitialized   bool
	subscribers       map[chan []byte]struct{}
	broadcastDebounce *time.Timer
}

var globalTracker *Tracker
var once sync.Once

// GetTracker returns the singleton UsageTracker instance.
func GetTracker() *Tracker {
	once.Do(func() {
		globalTracker = NewTracker()
	})
	return globalTracker
}

// NewTracker creates a new Tracker instance.
func NewTracker() *Tracker {
	return &Tracker{
		byModel:        make(map[string]int),
		byAccount:      make(map[string]map[string]int),
		requestedByKey: make(map[string]pendingNames),
		recentRing:     make([]RecentRequest, 0, ringCap),
		subscribers:    make(map[chan []byte]struct{}),
	}
}

// pendingNames are the names behind one in-flight request: what the client
// asked for, and the combo that served it when one did.
type pendingNames struct {
	Requested string
	Combo     string
}

// TrackPending updates the in-flight count for a given model, provider, and connection.
func (t *Tracker) TrackPending(model, provider, connectionID string, started bool, isError bool) {
	t.TrackPendingRequested("", "", model, provider, connectionID, started, isError)
}

// TrackPendingRequested additionally records the names the client asked for and
// the combo that served it. A combo request asks for the combo and is served by
// a member, so both names describe one in-flight request: they share the same
// counter and key, and they ride along for as long as that counter is above
// zero.
func (t *Tracker) TrackPendingRequested(requested, combo, model, provider, connectionID string, started bool, isError bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	modelKey := model
	if provider != "" {
		modelKey = fmt.Sprintf("%s (%s)", model, provider)
	}

	delta := 1
	if !started {
		delta = -1
	}

	// Update byModel
	newModelCount := t.byModel[modelKey] + delta
	if newModelCount <= 0 {
		delete(t.byModel, modelKey)
	} else {
		t.byModel[modelKey] = newModelCount
	}

	// Pair the names with the counter. Started sets them, zero clears them: a
	// stale pairing would keep calling a finished combo request a combo.
	if started && newModelCount > 0 && (requested != "" || combo != "") {
		if t.requestedByKey == nil {
			t.requestedByKey = make(map[string]pendingNames)
		}
		t.requestedByKey[modelKey] = pendingNames{Requested: requested, Combo: combo}
	}
	if newModelCount <= 0 && t.requestedByKey != nil {
		delete(t.requestedByKey, modelKey)
	}

	// Update byAccount (track public/direct requests under "__direct__" so they are never dropped)
	connKey := connectionID
	if connKey == "" {
		connKey = "__direct__"
	}
	accountMap, ok := t.byAccount[connKey]
	if !ok && started {
		accountMap = make(map[string]int)
		t.byAccount[connKey] = accountMap
	}
	if accountMap != nil {
		newAccCount := accountMap[modelKey] + delta
		if newAccCount <= 0 {
			delete(accountMap, modelKey)
			if len(accountMap) == 0 {
				delete(t.byAccount, connKey)
			}
		} else {
			accountMap[modelKey] = newAccCount
		}
	}

	if !started && isError && provider != "" {
		t.lastErrorProvider = provider
		t.lastErrorTs = time.Now().UnixMilli()
	}

	t.scheduleBroadcastLocked(nil)
}

// PushRecent adds a completed request to the ring buffer and notifies subscribers.
func (t *Tracker) PushRecent(req RecentRequest, repo *db.Repo) {
	t.ensureRingInitialized(repo)

	t.mu.Lock()
	defer t.mu.Unlock()

	// Prepend to ring
	if len(t.recentRing) >= ringCap {
		t.recentRing = append([]RecentRequest{req}, t.recentRing[:ringCap-1]...)
	} else {
		t.recentRing = append([]RecentRequest{req}, t.recentRing...)
	}

	t.scheduleBroadcastLocked(repo)
}

// ensureRingInitialized seeds the in-memory ring from DB history once under a write lock.
func (t *Tracker) ensureRingInitialized(repo *db.Repo) {
	if repo == nil {
		return
	}
	t.mu.RLock()
	initialized := t.ringInitialized
	t.mu.RUnlock()
	if initialized {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ringInitialized {
		return
	}
	t.ringInitialized = true
	// The ring is a best-effort seed: a failed read leaves it empty and live
	// pushes refill it. That is the right behaviour, but it must not be silent
	// — an empty ring is otherwise indistinguishable from a fresh install.
	if rows, err := repo.GetRecentUsageHistory(ringCap); err != nil {
		log.Warn("usage ring seed failed", "err", err)
	} else {
		seeded := make([]RecentRequest, 0, len(rows))
		for _, rh := range rows {
			seeded = append(seeded, recentFromHistoryRow(rh))
		}
		t.recentRing = append(t.recentRing, seeded...)
		if len(t.recentRing) > ringCap {
			t.recentRing = t.recentRing[:ringCap]
		}
	}
}

// GetActiveState computes the current active state for SSE streaming.
func (t *Tracker) GetActiveState(repo *db.Repo) StreamPayload {
	t.ensureRingInitialized(repo)

	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.buildPayloadLocked(repo)
}

func (t *Tracker) buildPayloadLocked(repo *db.Repo) StreamPayload {
	// Build connection name map
	connMap := make(map[string]string)
	if repo != nil {
		if conns, err := repo.GetProviderConnections("", true); err == nil {
			for _, c := range conns {
				name := c.ID
				if c.Name != nil && *c.Name != "" {
					name = *c.Name
				} else if c.Email != nil && *c.Email != "" {
					name = *c.Email
				}
				connMap[c.ID] = name
			}
		}
	}
	var active []ActiveRequest
	accountCountedModels := make(map[string]int)
	for connID, models := range t.byAccount {
		accName := connMap[connID]
		if connID == "__direct__" || connID == "" {
			accName = "Public / Direct"
		} else if accName == "" {
			if len(connID) > 8 {
				accName = fmt.Sprintf("Account %s...", connID[:8])
			} else {
				accName = "Account " + connID
			}
		}
		for modelKey, count := range models {
			if count > 0 {
				mName, pName := parseModelKey(modelKey)
				accountCountedModels[modelKey] += count
				active = append(active, ActiveRequest{
					Model:          mName,
					Provider:       pName,
					Account:        accName,
					Count:          count,
					RequestedModel: t.requestedByKey[modelKey].Requested,

					ComboName: t.requestedByKey[modelKey].Combo,
				})
			}
		}
	}

	// If any active requests were in byModel but not captured in byAccount, include them as Public / Direct
	for modelKey, totalCount := range t.byModel {
		alreadyCounted := accountCountedModels[modelKey]
		remaining := totalCount - alreadyCounted
		if remaining > 0 {
			mName, pName := parseModelKey(modelKey)
			active = append(active, ActiveRequest{
				Model:          mName,
				Provider:       pName,
				Account:        "Public / Direct",
				Count:          remaining,
				RequestedModel: t.requestedByKey[modelKey].Requested,

				ComboName: t.requestedByKey[modelKey].Combo,
			})
		}
	}

	recent := make([]RecentRequest, 0, len(t.recentRing))
	seen := make(map[string]bool)
	for _, r := range t.recentRing {
		if r.PromptTokens == 0 && r.CompletionTokens == 0 {
			continue
		}
		min := ""
		if len(r.Timestamp) >= 16 {
			min = r.Timestamp[:16]
		}
		k := fmt.Sprintf("%s|%s|%d|%d|%s", r.Model, r.Provider, r.PromptTokens, r.CompletionTokens, min)
		if !seen[k] {
			seen[k] = true
			recent = append(recent, r)
			if len(recent) >= 20 {
				break
			}
		}
	}

	errProv := ""
	if time.Now().UnixMilli()-t.lastErrorTs < 10000 {
		errProv = t.lastErrorProvider
	}

	byModelCopy := make(map[string]int, len(t.byModel))
	for k, v := range t.byModel {
		byModelCopy[k] = v
	}

	byAccountCopy := make(map[string]map[string]int, len(t.byAccount))
	for k, v := range t.byAccount {
		inner := make(map[string]int, len(v))
		for ik, iv := range v {
			inner[ik] = iv
		}
		byAccountCopy[k] = inner
	}

	return StreamPayload{
		ActiveRequests: active,
		RecentRequests: recent,
		ErrorProvider:  errProv,
		Pending: PendingState{
			ByModel:   byModelCopy,
			ByAccount: byAccountCopy,
		},
	}
}

// recentFromHistoryRow maps a persisted usageHistory row onto the stream shape
// so ring seeding and live pushes render identically.
func recentFromHistoryRow(rh db.UsageHistoryRow) RecentRequest {
	promptTok, complTok := translator.ResolveTokensCounts([]byte(rh.Tokens), rh.PromptTokens, rh.CompletionTokens)
	cached := translator.CachedTokensFromJSON([]byte(rh.Tokens))

	status := "ok"
	if rh.Status != "" && rh.Status != "success" && rh.Status != "ok" {
		status = rh.Status
	}

	return RecentRequest{
		Timestamp:        rh.Timestamp,
		Model:            rh.Model,
		Provider:         rh.Provider,
		PromptTokens:     int(promptTok),
		CompletionTokens: int(complTok),
		CachedTokens:     cached,
		Status:           status,
	}
}

func parseModelKey(key string) (model, provider string) {
	// key format: "modelName (providerName)"
	if idx := strings.LastIndex(key, " ("); idx != -1 && strings.HasSuffix(key, ")") {
		return key[:idx], key[idx+2 : len(key)-1]
	}
	return key, "unknown"
}

// Subscribe registers a channel to receive encoded SSE payload events.
func (t *Tracker) Subscribe() (chan []byte, func()) {
	ch := make(chan []byte, 16)
	t.mu.Lock()
	t.subscribers[ch] = struct{}{}
	t.mu.Unlock()

	unsubscribe := func() {
		t.mu.Lock()
		delete(t.subscribers, ch)
		close(ch)
		t.mu.Unlock()
	}

	return ch, unsubscribe
}

func (t *Tracker) scheduleBroadcastLocked(repo *db.Repo) {
	if t.broadcastDebounce != nil {
		t.broadcastDebounce.Stop()
	}

	t.broadcastDebounce = time.AfterFunc(50*time.Millisecond, func() {
		t.ensureRingInitialized(repo)
		t.mu.RLock()
		defer t.mu.RUnlock()

		payload := t.buildPayloadLocked(repo)
		b, err := json.Marshal(payload)
		if err != nil {
			return
		}

		// The send stays under the read lock: unsubscribe closes the channel
		// while holding the write lock, and a send racing that close panics
		// with "send on closed channel". Snapshotting the channels first and
		// sending after RUnlock left exactly that window open — the map was
		// read safely, the channel itself was not.
		for ch := range t.subscribers {
			select {
			case ch <- b:
			default:
			}
		}
	})
}
