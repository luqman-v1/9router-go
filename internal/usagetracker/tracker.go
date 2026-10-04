package usagetracker

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/translator"
	"9router/proxy/internal/log"
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
	mu                sync.RWMutex
	byModel           map[string]int
	byAccount         map[string]map[string]int
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
		byModel:     make(map[string]int),
		byAccount:   make(map[string]map[string]int),
		recentRing:  make([]RecentRequest, 0, ringCap),
		subscribers: make(map[chan []byte]struct{}),
	}
}

// TrackPending updates the in-flight count for a given model, provider, and connection.
func (t *Tracker) TrackPending(model, provider, connectionID string, started bool, isError bool) {
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
					Model:    mName,
					Provider: pName,
					Account:  accName,
					Count:    count,
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
				Model:    mName,
				Provider: pName,
				Account:  "Public / Direct",
				Count:    remaining,
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
	cached := translator.CachedTokensFromJSON([]byte(rh.Tokens))

	status := "ok"
	if rh.Status != "" && rh.Status != "success" && rh.Status != "ok" {
		status = rh.Status
	}

	return RecentRequest{
		Timestamp:        rh.Timestamp,
		Model:            rh.Model,
		Provider:         rh.Provider,
		PromptTokens:     rh.PromptTokens,
		CompletionTokens: rh.CompletionTokens,
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
