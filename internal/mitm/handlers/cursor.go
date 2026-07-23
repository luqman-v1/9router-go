package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
)

// HandleCursor intercepts Cursor IDE requests and forwards to 9router.
func HandleCursor(w http.ResponseWriter, r *http.Request, body []byte) {
	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		SendError(w, http.StatusBadRequest, "invalid Cursor request body")
		return
	}

	model, _ := reqBody["model"].(string)
	if model != "" && !strings.Contains(model, "/") {
		reqBody["model"] = "cursor/" + model
	}
	forwardBody, err := json.Marshal(reqBody)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "failed to marshal Cursor request")
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
