package chat

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/codexquota"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"

	"github.com/samber/lo"
)

var (
	errCodexQuotaInputs = errors.New("codex_quota: connectionID and accessToken are required")
	errCodexQuotaEmpty  = errors.New("codex_quota: wham usage returned no snapshot")
)

// shortConnID trims a connection id for log lines.
func shortConnID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func isNaNOrInf(f float64) bool { return math.IsNaN(f) || math.IsInf(f, 0) }

func parseFloatStrict(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || isNaNOrInf(f) {
		return 0, false
	}
	return f, true
}

// Codex quota-aware routing, mirroring the Antigravity shape in
// antigravity_quota.go: a process-local per-connection cache consulted by the
// connection pre-filters in connections.go so an exhausted account is skipped
// before a request is spent on it.
//
// Codex quota is account-level, not per-model: one 5h and one 7d window covers
// every model the account can serve. The cache is therefore keyed by connection
// only, which is also what makes it a better fit than the per-connection
// modelLock row for this provider — a model-keyed lock would free-route
// gpt-6-sol while gpt-5.5-codex on the same account is equally dead.

// CodexQuotaWindow is one cached window: percentage remaining and when it
// resets. A zero ResetAt means "exhausted but we do not know until when".
type CodexQuotaWindow struct {
	RemainingPercentage float64
	ResetAt             time.Time
}

// CodexConnectionQuota is the cached reading for one Codex account.
type CodexConnectionQuota struct {
	Session CodexQuotaWindow
	Weekly  CodexQuotaWindow
	// HasSession/HasWeekly record whether the account actually exposes the
	// window. An absent window is not an exhausted one and must not block.
	HasSession bool
	HasWeekly  bool
	Plan       string
}

var (
	codexQuotaMu       sync.RWMutex
	codexQuotaCache    = make(map[string]CodexConnectionQuota) // connectionID -> quota
	codexLastRefreshAt = make(map[string]time.Time)            // connectionID -> last refresh
	codexInflightMu    sync.Mutex
	codexInflight      = make(map[string]chan struct{}) // connectionID -> in-flight barrier

	// MinCodexQuotaRefreshInterval prevents hammering wham/usage during a 429
	// burst, matching MinAntigravityQuotaRefreshInterval.
	MinCodexQuotaRefreshInterval = 30 * time.Second
)

// codexQuotaExhausted reports whether a cached window means "do not route here
// until reset". Same predicate as quotaEntryExhausted: zero remaining with a
// reset still in the future.
func (w CodexQuotaWindow) codexQuotaExhausted(now time.Time) bool {
	return w.RemainingPercentage <= 0 && !w.ResetAt.IsZero() && w.ResetAt.After(now)
}

// ClearCodexQuotaCache resets the in-memory cache (unit tests).
func ClearCodexQuotaCache() {
	codexQuotaMu.Lock()
	defer codexQuotaMu.Unlock()
	codexQuotaCache = make(map[string]CodexConnectionQuota)
	codexLastRefreshAt = make(map[string]time.Time)
}

// ClearCodexQuotaBlock drops the cached reading for one connection. Called
// when a request on that connection succeeds, which proves the account is
// usable again — matching how the success path unlocks model locks.
func ClearCodexQuotaBlock(connectionID string) {
	if connectionID == "" {
		return
	}
	codexQuotaMu.Lock()
	defer codexQuotaMu.Unlock()
	delete(codexQuotaCache, connectionID)
	delete(codexLastRefreshAt, connectionID)
}

// IsCodexConnectionExhausted reports whether the connection must be skipped
// until one of its windows resets. Codex quota is account-level, so an
// exhausted 5h or 7d window blocks the whole account — mirroring the way
// antigravity's session window blocks a whole model family.
func IsCodexConnectionExhausted(connectionID string) bool {
	if connectionID == "" {
		return false
	}
	codexQuotaMu.RLock()
	q, ok := codexQuotaCache[connectionID]
	codexQuotaMu.RUnlock()
	if !ok {
		return false
	}
	now := time.Now().UTC()
	return (q.HasSession && q.Session.codexQuotaExhausted(now)) ||
		(q.HasWeekly && q.Weekly.codexQuotaExhausted(now))
}

// BlockCodexConnectionUntil caches an exhausted reading for a connection
// without contacting wham/usage. Used when the upstream 429 body already
// carries the authoritative reset time.
func BlockCodexConnectionUntil(connectionID string, resetAt time.Time) {
	if connectionID == "" || resetAt.IsZero() || !resetAt.After(time.Now().UTC()) {
		return
	}
	codexQuotaMu.Lock()
	defer codexQuotaMu.Unlock()
	q := codexQuotaCache[connectionID]
	q.Session = CodexQuotaWindow{RemainingPercentage: 0, ResetAt: resetAt}
	q.HasSession = true
	codexQuotaCache[connectionID] = q
	log.Warn("codex_quota", "connection blocked until reset", "conn", shortConnID(connectionID), "resetAt", resetAt.Format(time.RFC3339))
}

// filterCodexQuotaExhausted drops the Codex accounts whose cached window is
// spent. The 429 failover rotates over the whole candidate list and the
// least-recently-used pick stamps whichever row it reaches, so a parked
// account consumed the rotation's stamp — and a resolver pass — for a request
// it could never serve. An empty result keeps the input: the selector still
// refuses those rows one by one, so the all-parked case keeps its existing
// error path instead of turning into "no available connections".
func filterCodexQuotaExhausted(provider string, conns []*models.ProviderConnection) []*models.ProviderConnection {
	if provider != "codex" {
		return conns
	}
	live := lo.Filter(conns, func(c *models.ProviderConnection, _ int) bool {
		return c != nil && !IsCodexConnectionExhausted(c.ID)
	})
	if len(live) == 0 {
		return conns
	}
	return live
}

// RefreshCodexQuota reads live quota for a connection and caches it, so the
// picker skips an exhausted account before spending a request on it. In-flight
// refreshes coalesce and a 30s floor keeps a 429 burst from amplifying.
//
// Fail-open, as upstream: a 401/403 or any other error preserves the last
// known reading rather than overwriting it with "no quota".
func RefreshCodexQuota(ctx context.Context, client *http.Client, connectionID, accessToken string) (CodexConnectionQuota, error) {
	if connectionID == "" || accessToken == "" {
		return CodexConnectionQuota{}, errCodexQuotaInputs
	}
	now := time.Now().UTC()

	// Coalesce: a second caller waits for the in-flight fetch and reuses it.
	codexInflightMu.Lock()
	if ch, running := codexInflight[connectionID]; running {
		codexInflightMu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return CodexConnectionQuota{}, ctx.Err()
		}
		codexQuotaMu.RLock()
		defer codexQuotaMu.RUnlock()
		return codexQuotaCache[connectionID], nil
	}

	codexQuotaMu.RLock()
	lastRefresh := codexLastRefreshAt[connectionID]
	cached, hasCached := codexQuotaCache[connectionID]
	codexQuotaMu.RUnlock()
	if hasCached && now.Sub(lastRefresh) < MinCodexQuotaRefreshInterval {
		codexInflightMu.Unlock()
		return cached, nil
	}

	barrier := make(chan struct{})
	codexInflight[connectionID] = barrier
	codexInflightMu.Unlock()
	defer func() {
		codexInflightMu.Lock()
		delete(codexInflight, connectionID)
		close(barrier)
		codexInflightMu.Unlock()
	}()

	// Stamp the attempt, not the success: a failing wham/usage must not let a
	// 429 burst re-hit it once per request.
	codexQuotaMu.Lock()
	codexLastRefreshAt[connectionID] = now
	codexQuotaMu.Unlock()

	usage, err := codexquota.Fetch(ctx, client, accessToken)
	if err != nil {
		log.Warn("codex_quota", "refresh failed", "conn", shortConnID(connectionID), "error", err)
		return CodexConnectionQuota{}, err
	}
	if usage == nil {
		return CodexConnectionQuota{}, errCodexQuotaEmpty
	}

	fresh := codexQuotaFromUsage(usage)

	codexQuotaMu.Lock()
	// A block synthesized from a 429 body must survive a fresh, optimistic
	// wham reading until its reset actually passes.
	fresh = codexApplyActiveBlock(connectionID, fresh, now)
	codexQuotaCache[connectionID] = fresh
	codexQuotaMu.Unlock()
	return fresh, nil
}

// codexQuotaFromUsage converts a wham snapshot into the cached shape.
func codexQuotaFromUsage(usage *codexquota.Usage) CodexConnectionQuota {
	q := CodexConnectionQuota{Plan: usage.Plan}
	if usage.Session != nil {
		q.Session = CodexQuotaWindow{
			RemainingPercentage: usage.Session.Remaining(),
			ResetAt:             codexResetOrZero(usage.Session.ResetAt),
		}
		q.HasSession = true
	}
	if usage.Weekly != nil {
		q.Weekly = CodexQuotaWindow{
			RemainingPercentage: usage.Weekly.Remaining(),
			ResetAt:             codexResetOrZero(usage.Weekly.ResetAt),
		}
		q.HasWeekly = true
	}
	return q
}

// codexApplyActiveBlock re-asserts a block that has not yet expired. A 429
// proved the window closed even if wham/usage still reads optimistic, so an
// optimistic refresh must not resurrect the connection. Caller holds
// codexQuotaMu.
func codexApplyActiveBlock(connectionID string, fresh CodexConnectionQuota, now time.Time) CodexConnectionQuota {
	prev, ok := codexQuotaCache[connectionID]
	if !ok || !prev.HasSession {
		return fresh
	}
	if prev.Session.codexQuotaExhausted(now) && !fresh.Session.codexQuotaExhausted(now) {
		fresh.Session = prev.Session
		fresh.HasSession = true
	}
	return fresh
}

func codexResetOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
