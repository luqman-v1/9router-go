package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"9router/proxy/internal/db"
)

// ChatHandler handles /v1/chat/completions (OpenAI) and /v1/messages (Claude) endpoints.
type ChatHandler struct {
	Repo       *db.Repo
	Client     *http.Client
	RTKEnabled bool
	rrMu       sync.Mutex
	rrIdx      int // round-robin index
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
	APIKey      string `json:"apiKey"`
	AccessToken string `json:"accessToken"`
	BaseURL     string `json:"baseUrl,omitempty"`
}

// ProviderConfig describes how to reach an upstream provider.
type ProviderConfig struct {
	BaseURL       string
	AuthHeader    string            // "Authorization" or "x-api-key"
	AuthScheme    string            // "bearer" or "raw"
	NoAuth        bool              // true = no API key required
	DefaultAPIKey string            // fallback API key when none provided
	StaticHeaders map[string]string // extra headers to set on every request
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

// upstreamError captures a non-200 upstream response so the caller can
// retry with a different model (combo fallback) or write it to the client.
type upstreamError struct {
	StatusCode int
	Body       []byte
}

func (e *upstreamError) Error() string {
	return fmt.Sprintf("upstream returned %d", e.StatusCode)
}
