package handlers

import (
	"encoding/json"
	"net/http"
)

// updateModelInBody returns a copy of body with the "model" field set to modelName.
func updateModelInBody(body []byte, modelName string) []byte {
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

// setAuthHeader applies the provider's auth scheme to the request.
func setAuthHeader(req *http.Request, apiKey string, cfg *ProviderConfig) {
	switch cfg.AuthScheme {
	case "bearer":
		req.Header.Set(cfg.AuthHeader, "Bearer "+apiKey)
	case "raw":
		req.Header.Set(cfg.AuthHeader, apiKey)
	default:
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}
