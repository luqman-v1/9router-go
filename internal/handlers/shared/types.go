package shared

import (
	"net/http"
	"strings"

	"9router/proxy/internal/proxy"
)

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
	Strategy     string   // combo routing strategy: "fallback", "round-robin", "sticky", "fusion"
	StickyLimit  int      // sticky round-robin: consecutive requests per model before rotating (default 1)
}

// ConnectionData holds parsed fields from the providerConnections.data JSON blob.
type ConnectionData struct {
	APIKey               string                 `json:"apiKey"`
	AccessToken          string                 `json:"accessToken"`
	BaseURL              string                 `json:"baseUrl,omitempty"`
	ProxyPoolID          string                 `json:"proxyPoolId,omitempty"`
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

// StreamMetrics captures timing and content during a proxied stream.
type StreamMetrics struct {
	TTFT        int64           // ms from request start to first chunk
	ResponseBuf strings.Builder // accumulated response content
}

// UpstreamError is an alias for proxy.UpstreamError — retryable errors from upstream.
type UpstreamError = proxy.UpstreamError

// StatusWriter intercepts HTTP status code for logging.
type StatusWriter struct {
	http.ResponseWriter
	Status int
}

func (w *StatusWriter) WriteHeader(status int) {
	w.Status = status
	w.ResponseWriter.WriteHeader(status)
}
