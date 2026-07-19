package handlers

import (
	"net/http"
	"strings"
	"sync"

	"9router/proxy/internal/db"
	"9router/proxy/internal/proxy"
)

// ChatHandler handles /v1/chat/completions (OpenAI) and /v1/messages (Claude) endpoints.
type ChatHandler struct {
	Repo        *db.Repo
	Client      *http.Client
	TokenSaver  *TokenSaverConfig
	rrMu        sync.Mutex
	rrIdx       int // round-robin index
}

// ModelInfo holds the resolved provider and model identifiers.
// ConnectionID, when set, pins a specific connection found during resolution
// so getBestConnection can skip the DB lookup.
// ComboModels, when non-empty, lists all "provider/model" entries from a combo.
// The handler iterates through them on upstream failure.
type ModelInfo struct {
	Provider     string
	Model        string
	ConnectionID string   // optional — set when the resolver already found a connection
	ComboModels  []string // non-empty when resolved from a combo; each entry is "provider/model"
	Strategy     string   // combo routing strategy: "fallback", "round-robin", "capacity", "fusion"
}

// ConnectionData holds parsed fields from the providerConnections.data JSON blob.
type ConnectionData struct {
	APIKey              string                 `json:"apiKey"`
	AccessToken         string                 `json:"accessToken"`
	BaseURL             string                 `json:"baseUrl,omitempty"`
	ProviderSpecificData map[string]interface{} `json:"providerSpecificData,omitempty"`
}

// UsageLogInfo holds request context needed to log a usage record.
type UsageLogInfo struct {
	Provider     string
	Model        string
	ConnectionID string
	APIKey       string
	Endpoint     string
}

// streamMetrics captures timing and content during a proxied stream.
type streamMetrics struct {
	ttft        int64            // ms from request start to first chunk
	responseBuf strings.Builder  // accumulated response content
}

// upstreamError is an alias for proxy.UpstreamError — retryable errors from upstream.
type upstreamError = proxy.UpstreamError
