package handlerutil

import (
	"encoding/json"
	"fmt"
	"net/http"

	"9router/proxy/internal/constants"
)

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
	jsonBytes, err := json.Marshal(errResp)
	if err != nil {
		w.Write([]byte(`{"error":{"message":"internal error","type":"server_error","code":"internal_server_error"}}`))
		return
	}
	w.Write(jsonBytes)
}

// WriteJSON writes a JSON response with the given status code and body.
func WriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
	w.WriteHeader(status)
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		w.Write([]byte(`{"error":{"message":"internal error","type":"invalid_request_error","code":500}}`))
		return
	}
	w.Write(jsonBytes)
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
