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
	JudgeModel   string   // optional judge model for fusion strategy
}

// ConnectionData holds parsed fields from the providerConnections.data JSON blob.
type ConnectionData struct {
	APIKey                 string         `json:"apiKey"`
	AccessToken            string         `json:"accessToken"`
	BaseURL                string         `json:"baseUrl,omitempty"`
	ProxyPoolID            string         `json:"proxyPoolId,omitempty"`
	ConnectionProxyEnabled bool           `json:"connectionProxyEnabled,omitempty"`
	ConnectionProxyURL     string         `json:"connectionProxyUrl,omitempty"`
	ConnectionNoProxy      string         `json:"connectionNoProxy,omitempty"`
	StrictProxy            bool           `json:"strictProxy,omitempty"`
	ProviderSpecificData   map[string]any `json:"providerSpecificData,omitempty"`

	// ResolvedProxyPool is the egress label for this request's pool — the name an
	// operator gave it in the dashboard ("Vercel Relay"). Filled in by the client
	// resolver, which already looked the pool up, so a log line can name the pool
	// instead of printing a UUID nobody recognises. Set per request rather than
	// persisted, so a rename shows up in the next log line.
	ResolvedProxyPool string `json:"-"`
}

// UsageLogInfo holds request context needed to log a usage record. ConnName and
// ConnEmail identify the account that actually served the turn: with several
// connections behind one provider, provider+model says nothing about which
// key or subscription paid for it.
type UsageLogInfo struct {
	Provider     string
	Model        string
	ConnectionID string
	ConnName     string
	ConnEmail    string
	APIKey       string
	Endpoint     string
	// Egress is the proxy pool or relay a request left the host through, empty
	// for a direct one. Without it the log cannot answer "did this turn use the
	// proxy?", which is the first question asked whenever a provider's rate limit
	// or block list is tied to an IP.
	Egress string

	// RTK token compression metrics
	OriginalInputTokens int
	SavedTokens         int
	SavedPercent        int
}

// ConnIdentityKV returns the log key/value pairs naming the account behind a
// request. A request with no stored connection (a no-auth provider dialled
// directly) is reported as "Public / Direct" rather than as an empty field, so
// a reader can always tell "which account" apart from "no account".
func (u *UsageLogInfo) ConnIdentityKV() []any {
	kv := make([]any, 0, 8)
	if u.ConnectionID != "" {
		kv = append(kv, "conn", u.ConnectionID)
	}
	if u.ConnName != "" {
		kv = append(kv, "connName", u.ConnName)
	}
	if u.ConnEmail != "" {
		kv = append(kv, "email", u.ConnEmail)
	}
	if u.Egress != "" {
		kv = append(kv, "egress", u.Egress)
	}
	if len(kv) == 0 {
		kv = append(kv, "account", "Public / Direct")
	}
	return kv
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

// Len returns the number of captured bytes.
func (b *ResponseBuf) Len() int {
	return len(b.buf)
}

// Bytes returns the captured slice.
func (b *ResponseBuf) Bytes() []byte {
	return b.buf
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
