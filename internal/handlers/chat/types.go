package chat

import (
	"net/http"
	"sync"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/models"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/semanticcache"
)

type comboStickyState struct {
	Index               int
	ConsecutiveUseCount int
	// ServingIndex is the model index currently owning the turn. Mid-turn
	// requests reuse it so a tool-use sequence stays on the same provider,
	// even after Index has advanced for the next turn.
	ServingIndex int
}

// smartStickyEntry is one session's pinned model in the smart strategy.
type smartStickyEntry struct {
	Model    string
	LastUsed time.Time
}

// ChatHandler handles /v1/chat/completions (OpenAI) and /v1/messages (Claude) endpoints.
type ChatHandler struct {
	Repo       *db.Repo
	Client     *http.Client
	TokenSaver *shared.TokenSaverConfig
	// RateLimiter is the process-wide limiter the request path charges against.
	// The handler holds it so the metering path can reconcile a TPM
	// reservation once the real usage is known: the estimate is taken
	// pre-dispatch, but only the response knows what the turn actually cost.
	RateLimiter   *middleware.RateLimiter
	SemanticCache *semanticcache.Cache
	stickyMu      sync.Mutex
	stickyState   map[string]*comboStickyState

	// deprecationCache throttles kv writes when a provider retires a model:
	// every request at a dead combo entry arrives as a fresh 410, and each
	// one would otherwise upsert the same row.
	deprecationMu    sync.Mutex
	deprecationCache map[string]time.Time
	// smartSticky pins the model a session's smart-routed combo settles on,
	// so later turns reuse the provider holding the warm prompt cache.
	smartStickyMu sync.Mutex
	smartSticky   map[string]smartStickyEntry
	// timeoutBench holds, per combo member, when its response-timeout grace
	// period ends. See benchTimedOutModel.
	timeoutBenchMu sync.Mutex
	timeoutBench   map[string]time.Time
	// SessionAffinity pins client sessions to upstream connection accounts.
	sessionAffinityMu sync.Mutex
	SessionAffinity   *SessionAffinityStore
}

// Type aliases for shared types
type ModelInfo = shared.ModelInfo

// ProviderConnection is the stored connection row.
type ProviderConnection = models.ProviderConnection
type ConnectionData = shared.ConnectionData
type UsageLogInfo = shared.UsageLogInfo
type streamMetrics = shared.StreamMetrics
type upstreamError = proxy.UpstreamError
