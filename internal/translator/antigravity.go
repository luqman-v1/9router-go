package translator

import (
	"encoding/json"
	"fmt"
	"time"

	"9router/proxy/internal/log"
)

// AntigravityRequest is the wrapper format for Antigravity API.
type AntigravityRequest struct {
	Project     string          `json:"project"`
	Model       string          `json:"model"`
	UserAgent   string          `json:"userAgent"`
	RequestType string          `json:"requestType"`
	RequestID   string          `json:"requestId"`
	Request     json.RawMessage `json:"request"`
}

// WrapForAntigravity wraps a standard Gemini request in Antigravity API envelope.
func WrapForAntigravity(geminiBody []byte, projectID, modelName string) ([]byte, error) {
	wrapper := AntigravityRequest{
		Project:     projectID,
		Model:       modelName,
		UserAgent:   "antigravity/ide/0.1",
		RequestType: "agent",
		RequestID:   fmt.Sprintf("agent/%s/%d/%s/%d", projectID, time.Now().UnixMilli(), modelName, 1),
		Request:     geminiBody,
	}
	out, err := json.Marshal(wrapper)
	if err != nil {
		return nil, fmt.Errorf("marshal antigravity wrapper: %w", err)
	}
	return out, nil
}

// UnwrapAntigravityResponse extracts the inner Gemini response from antigravity envelope.
func UnwrapAntigravityResponse(raw []byte) []byte {
	var envelope struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		log.Warn("translator", "unmarshal envelope failed", "error", err)
		return raw // passthrough on failure
	}
	if len(envelope.Response) == 0 {
		log.Warn("translator", "empty envelope response")
		return raw // passthrough on failure
	}
	return []byte(envelope.Response)
}
