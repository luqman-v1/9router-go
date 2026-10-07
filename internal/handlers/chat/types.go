package chat

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/models"
	"9router/proxy/internal/proxy"
)

type comboStickyState struct {
	Index               int
	ConsecutiveUseCount int
	// ServingIndex is the model index currently owning the turn. Mid-turn
	// requests reuse it so a tool-use sequence stays on the same provider,
	// even after Index has advanced for the next turn.
	ServingIndex int
}

// ChatHandler handles /v1/chat/completions (OpenAI) and /v1/messages (Claude) endpoints.
type ChatHandler struct {
	Repo        *db.Repo
	Client      *http.Client
	TokenSaver  *shared.TokenSaverConfig
	stickyMu    sync.Mutex
	stickyState map[string]*comboStickyState
	// oauthRefreshFlight collapses concurrent OAuth token refreshes for the
	// same connection, so an expired token triggers one upstream round-trip
	// instead of one per in-flight request.
	oauthRefreshFlight singleflight.Group
	// deprecationCache throttles kv writes when a provider retires a model:
	// every request at a dead combo entry arrives as a fresh 410, and each
	// one would otherwise upsert the same row.
	deprecationMu    sync.Mutex
	deprecationCache map[string]time.Time
}

// Type aliases for shared types
type ModelInfo = shared.ModelInfo

// ProviderConnection is the stored connection row.
type ProviderConnection = models.ProviderConnection
type ConnectionData = shared.ConnectionData
type UsageLogInfo = shared.UsageLogInfo
type streamMetrics = shared.StreamMetrics
type upstreamError = proxy.UpstreamError
