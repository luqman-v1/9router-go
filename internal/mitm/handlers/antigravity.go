package handlers

import (
	"encoding/json"
	"net/http"
)

// HandleAntigravity intercepts Antigravity Gemini-native requests and forwards to 9router.
func HandleAntigravity(w http.ResponseWriter, r *http.Request, body []byte) {
	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		SendError(w, http.StatusBadRequest, "invalid Antigravity request body")
		return
	}

	model, _ := reqBody["model"].(string)
	if model != "" {
		reqBody["model"] = "antigravity/" + model
	}
	reqBody["userAgent"] = "antigravity"
	forwardBody, _ := json.Marshal(reqBody)

	isStream := len(r.URL.Query().Get("alt")) > 0 || len(r.URL.Query().Get(":streamGenerateContent")) > 0

	upstream, err := FetchRouter(forwardBody, "/v1/chat/completions", r.Header, "")
	if err != nil {
		SendError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer upstream.Body.Close()

	if isStream || upstream.Header.Get("Content-Type") == "text/event-stream" {
		PipeSSE(upstream, w)
	} else {
		PipeJSON(upstream, w)
	}
}
