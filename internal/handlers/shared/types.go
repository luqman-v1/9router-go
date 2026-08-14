package shared

import (
	"net/http"

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
	APIKey                 string                 `json:"apiKey"`
	AccessToken            string                 `json:"accessToken"`
	BaseURL                string                 `json:"baseUrl,omitempty"`
	ProxyPoolID            string                 `json:"proxyPoolId,omitempty"`
	ConnectionProxyEnabled bool                   `json:"connectionProxyEnabled,omitempty"`
	ConnectionProxyURL     string                 `json:"connectionProxyUrl,omitempty"`
	ConnectionNoProxy      string                 `json:"connectionNoProxy,omitempty"`
	StrictProxy            bool                   `json:"strictProxy,omitempty"`
	ProviderSpecificData   map[string]interface{} `json:"providerSpecificData,omitempty"`
}

// UsageLogInfo holds request context needed to log a usage record.
type UsageLogInfo struct {
	Provider     string
	Model        string
	ConnectionID string
	APIKey       string
	Endpoint     string
}

// ResponseCaptureMax is the maximum response content retained for token
// estimation. Content beyond this is dropped — a huge upstream response must
// not balloon per-request memory just to estimate tokens.
const ResponseCaptureMax = 100_000

// ResponseBuf is a size-capped buffer for captured response content.
// It implements io.Writer and keeps only the first ResponseCaptureMax bytes.
type ResponseBuf struct {
	buf  []byte
	done bool
}

// Write appends p up to the cap, dropping anything beyond it.
func (b *ResponseBuf) Write(p []byte) (int, error) {
	if b.done {
		return len(p), nil
	}
	room := ResponseCaptureMax - len(b.buf)
	if room <= 0 {
		b.done = true
		return len(p), nil
	}
	if len(p) > room {
		b.buf = append(b.buf, p[:room]...)
		b.done = true
	} else {
		b.buf = append(b.buf, p...)
	}
	return len(p), nil
}

// String returns the captured content.
func (b *ResponseBuf) String() string {
	return string(b.buf)
}

// StreamMetrics captures timing and content during a proxied stream.
type StreamMetrics struct {
	TTFT        int64       // ms from request start to first chunk
	ResponseBuf ResponseBuf // accumulated response content (capped)
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
