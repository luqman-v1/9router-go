package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
)

// HandleKiro intercepts Kiro AWS EventStream requests and forwards to 9router.
func HandleKiro(w http.ResponseWriter, r *http.Request, body []byte) {
	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		SendError(w, http.StatusBadRequest, "invalid Kiro request body")
		return
	}

	model, _ := reqBody["model"].(string)
	if model != "" && !strings.Contains(model, "/") {
		reqBody["model"] = "kiro/" + model
	}
	forwardBody, err := json.Marshal(reqBody)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "failed to marshal Kiro request")
		return
	}

	upstream, err := FetchRouter(r.Context(), forwardBody, "/v1/chat/completions", r.Header, "")
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
