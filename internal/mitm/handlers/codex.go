package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
)

// HandleCodex intercepts Codex CLI requests and forwards to 9router Responses API.
func HandleCodex(w http.ResponseWriter, r *http.Request, body []byte) {
	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		SendError(w, http.StatusBadRequest, "invalid Codex request body")
		return
	}

	model, _ := reqBody["model"].(string)
	if model != "" && !strings.Contains(model, "/") {
		reqBody["model"] = "codex/" + model
	}
	forwardBody, err := json.Marshal(reqBody)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "failed to marshal Codex request")
		return
	}

	upstream, err := FetchRouter(r.Context(), forwardBody, "/v1/responses", r.Header, "")
	if err != nil {
		SendError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer upstream.Body.Close()

	if upstream.Header.Get("Content-Type") == "text/event-stream" {
		PipeSSE(upstream, w)
	} else {
		PipeJSON(upstream, w)
	}
}
