package executor

import (
	json "encoding/json/v2"
	"errors"
	"net/http"
	"strings"

	"9router/proxy/internal/proxy"
)

const copilotContextOverflowMsgPrefix = "Your input exceeds the context window of this model."
const ContextLengthExceededCode = "context_length_exceeded"

// ForwardGithub handles GitHub Copilot requests, normalizing Copilot context overflow
// errors (400 invalid_request_body with "Your input exceeds...") into standard
// context_length_exceeded error codes (upstream decolua/9router PR #5280 parity).
func ForwardGithub(w http.ResponseWriter, req *Request) error {
	err := ForwardOpenAI(w, req)
	if err == nil {
		return nil
	}

	var ue *proxy.UpstreamError
	if errors.As(err, &ue) && ue.StatusCode == http.StatusBadRequest {
		normalizeGithubContextError(ue)
	}

	return err
}

func normalizeGithubContextError(ue *proxy.UpstreamError) {
	if len(ue.Body) == 0 {
		return
	}

	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}

	if err := json.Unmarshal(ue.Body, &payload); err != nil {
		return
	}

	if payload.Error.Code == "invalid_request_body" && strings.HasPrefix(payload.Error.Message, copilotContextOverflowMsgPrefix) {
		payload.Error.Code = ContextLengthExceededCode
		payload.Error.Type = "invalid_request_error"
		if newBody, err := json.Marshal(payload); err == nil {
			ue.Body = newBody
		}
	}
}
