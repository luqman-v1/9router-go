package guardrails

import (
	"bytes"
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/middleware"
)

// maxScanBody caps how much of a request body is read for scanning. A prompt
// larger than this is not scanned rather than buffered without bound, so a
// hostile client cannot make the gateway hold an arbitrarily large body in
// memory on the guardrails path.
const maxScanBody = 8 << 20 // 8 MiB

// Audit records a decision that was taken. Implementations must be
// non-blocking: an audit sink that stalls cannot stall the gateway.
type Audit func(Decision, Target)

// Inbound scans the request body for policy violations before the handler
// sees it.
//
// It is a no-op unless a policy resolves to an enabled engine for this key and
// model, so an install that never configured guardrails pays one policy lookup
// and nothing else. When it does act, it either rejects the request or hands
// the next handler a rewritten body — the rewrite is the point of a mask
// action, so the body must be replaced rather than merely inspected.
func Inbound(store PolicyStore, audit Audit, sw Switch) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !sw.Enabled() {
				next.ServeHTTP(w, r)
				return
			}
			target := Target{APIKeyID: apiKeyID(r)}
			engine, err := Resolve(store, target)
			if err != nil || !engine.Enabled() {
				next.ServeHTTP(w, r)
				return
			}
			if r.Body == nil || r.ContentLength == 0 {
				next.ServeHTTP(w, r)
				return
			}

			body, err := io.ReadAll(io.LimitReader(r.Body, maxScanBody))
			_ = r.Body.Close()
			if err != nil {
				// An unreadable body is not this middleware's error to define;
				// let the real handler reject it.
				next.ServeHTTP(w, r)
				return
			}

			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil {
				// Non-JSON body (a proxy relay, a raw upload) has no text
				// structure to walk; the handler owns it.
				r.Body = io.NopCloser(bytes.NewReader(body))
				next.ServeHTTP(w, r)
				return
			}

			scanned, decision := engine.ScanJSON(payload)
			if decision.Action == ActionAllow {
				r.Body = io.NopCloser(bytes.NewReader(body))
				next.ServeHTTP(w, r)
				return
			}

			if audit != nil {
				audit(*decision, target)
			}
			if decision.Blocked() {
				handlerutil.WriteJSONError(
					w, http.StatusBadRequest,
					"Request blocked by a configured guardrail policy.",
				)
				return
			}
			if !decision.WasMutated() {
				// A log_only or warn policy fires without changing any value, and
				// re-marshalling an unchanged payload is not free: the JSON is
				// re-encoded from a Go map, so key order follows map iteration and
				// the provider receives a body that differs from the client's byte
				// for byte even though nothing was masked.
				r.Body = io.NopCloser(bytes.NewReader(body))
				next.ServeHTTP(w, r)
				return
			}

			rewritten, err := json.Marshal(scanned)
			if err != nil {
				// The scan mutated the payload into something that will not
				// re-marshal. Failing closed here would be a lie about what
				// the content was; pass the original through and let the
				// provider see unmasked data only if it really cannot be
				// encoded, which the handler will reject anyway.
				r.Body = io.NopCloser(bytes.NewReader(body))
				next.ServeHTTP(w, r)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(rewritten))
			r.ContentLength = int64(len(rewritten))
			r.Header.Set("Content-Length", itoa(len(rewritten)))
			next.ServeHTTP(w, r)
		})
	}
}

// apiKeyID reads the authenticated key id without importing the middleware
// package's auth types into this package's public surface.
func apiKeyID(r *http.Request) string {
	if k := middleware.GetAuthenticatedApiKey(r); k != nil {
		return k.ID
	}
	return ""
}

// itoa is strconv.Itoa without the import; the value is a body length.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// ExtractContent pulls the human-visible text out of a decoded chat message,
// so the outbound tap can scan the same shape the inbound tap rewrites.
func ExtractContent(m map[string]any) string {
	var b strings.Builder
	content, ok := m["content"].(string)
	if ok {
		b.WriteString(content)
	}
	parts, ok := m["content"].([]any)
	if !ok {
		return b.String()
	}
	for _, part := range parts {
		pm, ok := part.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := pm["text"].(string); ok {
			b.WriteString(text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}