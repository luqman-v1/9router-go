package handlerutil

import (
	"encoding/json"
	"net/http"
)

// WriteJSONError writes a standardized JSON error response.
func WriteJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	errResp := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"code":    status,
		},
	}
	jsonBytes, err := json.Marshal(errResp)
	if err != nil {
		w.Write([]byte(`{"error":{"message":"internal error","type":"invalid_request_error","code":500}}`))
		return
	}
	w.Write(jsonBytes)
}

// UpdateModelInBody returns a copy of body with the "model" field set to modelName.
func UpdateModelInBody(body []byte, modelName string) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
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
	switch authScheme {
	case "bearer":
		req.Header.Set(authHeader, "Bearer "+apiKey)
	case "raw":
		req.Header.Set(authHeader, apiKey)
	default:
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}
