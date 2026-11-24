package modelbreaker

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"sync"

	"9router/proxy/internal/errclass"
)

// The registry is keyed on provider plus model, with both lowercased: the same
// pair reaches this layer from a registered provider's connection, a custom
// provider node, and the dashboard, and those three paths do not always agree
// on case. A single mutex guards the map and each breaker locks itself, which
// is the same shape internal/resilience/open-sse uses so the two layers stay
// readable together.

var (
	registryMu sync.Mutex
	registry   = map[string]*Breaker{}
)

// Key builds the registry key for one provider/model pair.
func Key(provider, model string) string {
	return strings.ToLower(provider) + "/" + strings.ToLower(model)
}

// CountsTowardBreaker reports whether an upstream error is the model's own
// fault, which is the only case this layer may charge.
//
// The rule is errclass's Scope, not the status code: ScopeModel means the
// model itself is gone or the prompt does not fit it, and that is what a
// per-model breaker exists to catch. ScopeProvider (5xx, transport) is the
// provider breaker's job — charging it here would put every model of a
// briefly-down provider into cooldown and then disable all of them after
// three cycles. ScopeAccount (429, quota, auth) is the connection-rotation
// layer's job for the same reason. ScopeRequest cools nothing.
func CountsTowardBreaker(class errclass.Classification) bool {
	switch class.EffectiveScope() {
	case errclass.ScopeModel:
		return true
	default:
		return false
	}
}

// Classify turns an upstream status plus body into the classification this
// layer needs, for the call site that has not already classified the error.
//
// The body is read, not searched: providers carry their verdict in a JSON
// envelope ("error":{"type":…,"message":…,"code":…}), and errclass's precedence
// is type first, then that message, then the numeric status. Handing the raw
// bytes to ClassifyDetail as a message would skip the type and feed the JSON
// prose to the phrase table, so a typed model verdict would fall through to the
// status map and a 404 would be the only thing left to decide the scope.
func Classify(status int, body []byte) errclass.Classification {
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var env struct {
			Type  string          `json:"type"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(trimmed), &env) == nil {
			errRaw := bytes.TrimSpace(env.Error)
			if len(errRaw) > 0 && errRaw[0] == '{' {
				var detail struct {
					Type    string          `json:"type"`
					Message string          `json:"message"`
					Code    json.RawMessage `json:"code"`
					Status  string          `json:"status"`
				}
				if json.Unmarshal(errRaw, &detail) == nil {
					code := 0
					if codeFromRaw(detail.Code, &code) {
						return errclass.ClassifyDetail(detail.Type, code, detail.Status, detail.Message)
					}
					return errclass.ClassifyDetail(detail.Type, 0, detail.Status, detail.Message)
				}
			}
			// A bare {"error":"…"} envelope: the string is the message.
			if len(errRaw) > 0 && errRaw[0] == '"' {
				var msg string
				if json.Unmarshal(errRaw, &msg) == nil && msg != "" {
					return errclass.ClassifyDetail("", status, "", msg)
				}
			}
		}
	}
	return errclass.ClassifyDetail("", status, "", trimmed)
}

// codeFromRaw reads a numeric or numeric-string JSON code into out, reporting
// whether it found one. Non-numeric code values (a provider's own code enum)
// carry no status, so they leave out untouched and the status the caller passed
// decides.
func codeFromRaw(raw json.RawMessage, out *int) bool {
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return false
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		*out = n
		return true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			*out = v
			return true
		}
	}
	return false
}

// AllStatuses returns a snapshot of every tracked model, for the dashboard.
func AllStatuses() []Status {
	registryMu.Lock()
	breakers := make([]*Breaker, 0, len(registry))
	for _, b := range registry {
		breakers = append(breakers, b)
	}
	registryMu.Unlock()
	out := make([]Status, 0, len(breakers))
	for _, b := range breakers {
		out = append(out, b.Status())
	}
	return out
}

// ResetAll closes every breaker. Used by the dashboard's reset control and by
// tests.
func ResetAll() {
	registryMu.Lock()
	breakers := make([]*Breaker, 0, len(registry))
	for _, b := range registry {
		breakers = append(breakers, b)
	}
	registryMu.Unlock()
	for _, b := range breakers {
		b.Reset()
	}
}
