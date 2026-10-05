package handlerutil

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/http"

	"9router/proxy/internal/constants"
)

// deterministicJSON makes Go maps serialize with their keys in sorted order, so
// the same data always produces byte-identical JSON. Without it, encoding/json/v2
// marshals maps in the runtime's randomized iteration order, which makes list
// order shift between requests (e.g. the dashboard models list on every refresh).
var deterministicJSON = json.Deterministic(true)

// errorTypes maps HTTP status codes to OpenAI-compatible error types and codes.
var errorTypes = map[int]struct {
	errType string
	errCode string
}{
	http.StatusBadRequest:          {errType: "invalid_request_error", errCode: "bad_request"},
	http.StatusUnauthorized:        {errType: "authentication_error", errCode: "invalid_api_key"},
	http.StatusPaymentRequired:     {errType: "billing_error", errCode: "payment_required"},
	http.StatusForbidden:           {errType: "permission_error", errCode: "insufficient_quota"},
	http.StatusNotFound:            {errType: "invalid_request_error", errCode: "model_not_found"},
	http.StatusMethodNotAllowed:    {errType: "invalid_request_error", errCode: "method_not_allowed"},
	http.StatusNotAcceptable:       {errType: "invalid_request_error", errCode: "model_not_supported"},
	http.StatusTooManyRequests:     {errType: "rate_limit_error", errCode: "rate_limit_exceeded"},
	http.StatusInternalServerError: {errType: "server_error", errCode: "internal_server_error"},
	http.StatusBadGateway:          {errType: "server_error", errCode: "bad_gateway"},
	http.StatusServiceUnavailable:  {errType: "server_error", errCode: "service_unavailable"},
	http.StatusGatewayTimeout:      {errType: "server_error", errCode: "gateway_timeout"},
}

// WriteJSONError writes a standardized JSON error response with status-code-aware
// error types matching OpenAI API conventions.
func WriteJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
	w.WriteHeader(status)

	errType := "invalid_request_error"
	errCode := fmt.Sprintf("%d", status)
	if t, ok := errorTypes[status]; ok {
		errType = t.errType
		errCode = t.errCode
	}

	errResp := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"code":    errCode,
		},
	}
	if err := json.MarshalWrite(w, errResp, deterministicJSON); err != nil {
		w.Write([]byte(`{"error":{"message":"internal error","type":"server_error","code":"internal_server_error"}}`))
	}
}

// WriteJSON writes a JSON response directly to the ResponseWriter with zero intermediate byte buffering.
func WriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
	w.WriteHeader(status)
	if err := json.MarshalWrite(w, data, deterministicJSON); err != nil {
		w.Write([]byte(`{"error":{"message":"internal error","type":"invalid_request_error","code":500}}`))
	}
}

// WriteJSONIndented writes a JSON response indented with two spaces — the shape
// the upstream dashboard's backup download produces with JSON.stringify(payload,
// null, 2). A handler that hands the body to the user as a file uses it so the
// saved backup stays human-readable.
func WriteJSONIndented(w http.ResponseWriter, status int, data any) {
	w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
	w.WriteHeader(status)
	if err := json.MarshalWrite(w, data, deterministicJSON, jsontext.WithIndent("  ")); err != nil {
		w.Write([]byte(`{"error":{"message":"internal error","type":"invalid_request_error","code":500}}`))
	}
}

// UpdateModelInBody returns a copy of body with the "model" field set to modelName.
func UpdateModelInBody(body []byte, modelName string) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body
	}
	m["model"] = modelName
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// SetAuthHeader applies the provider's auth scheme to the request.
func SetAuthHeader(req *http.Request, apiKey, authHeader, authScheme string) {
	if authHeader == "" {
		authHeader = "Authorization"
	}
	switch authScheme {
	case "bearer":
		req.Header.Set(authHeader, "Bearer "+apiKey)
	case "raw":
		req.Header.Set(authHeader, apiKey)
	default:
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

// GetString safely extracts a string value from a map[string]any by key.
func GetString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// SessionHeaderKeys defines the client request header keys that carry session IDs in priority order.
var SessionHeaderKeys = []string{
	"x-claude-code-session-id",
	"x-session-id",
	"session-id",
	"session_id",
	"x-amp-thread-id",
}

// ExtractSessionID extracts the session identifier from incoming HTTP request headers.
func ExtractSessionID(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, key := range SessionHeaderKeys {
		if val := r.Header.Get(key); val != "" {
			return val
		}
	}
	return ""
}

type sessionIDKey struct{}

// WithSessionID returns a context carrying the given session ID.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	if sessionID == "" || ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, sessionIDKey{}, sessionID)
}

// GetSessionID retrieves the session ID from the context if present.
func GetSessionID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if val, ok := ctx.Value(sessionIDKey{}).(string); ok {
		return val
	}
	return ""
}

type clientBetaKey struct{}

// WithClientAnthropicBeta carries the caller's own anthropic-beta flags on the
// context. They have to be merged into the upstream request rather than dropped:
// a client asking for a beta the gateway does not list would otherwise fail
// without ever being told why. (Upstream mergeAnthropicBeta, v0.5.91.)
func WithClientAnthropicBeta(ctx context.Context, beta string) context.Context {
	if beta == "" || ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, clientBetaKey{}, beta)
}

// GetClientAnthropicBeta returns the caller's anthropic-beta flags, or "".
func GetClientAnthropicBeta(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if val, ok := ctx.Value(clientBetaKey{}).(string); ok {
		return val
	}
	return ""
}
