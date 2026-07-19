package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
)

// HandleCopilot intercepts GitHub Copilot requests and forwards to 9router chat completions.
func HandleCopilot(w http.ResponseWriter, r *http.Request, body []byte) {
	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		SendError(w, http.StatusBadRequest, "invalid Copilot request body")
		return
	}

	model, _ := reqBody["model"].(string)
	if model != "" && !strings.Contains(model, "/") {
		reqBody["model"] = "github/" + model
	}
	forwardBody, _ := json.Marshal(reqBody)

	upstream, err := FetchRouter(forwardBody, "/v1/chat/completions", r.Header, "")
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
