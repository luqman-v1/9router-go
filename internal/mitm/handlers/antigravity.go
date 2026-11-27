package handlers

import (
	json "encoding/json/v2"
	"net/http"
	"strings"
)

const (
	antigravityIDEVersion   = "2.33.0"
	antigravityUABarePrefix = "antigravity/"
	antigravityUAHubPrefix  = "antigravity/hub/"
)

// trailingSuffix returns the platform suffix (e.g. " darwin/arm64") portion
// of an Antigravity User-Agent after the version component.
func trailingSuffix(userAgent, prefix string) string {
	rest := userAgent[len(prefix):]
	sepIdx := strings.Index(rest, " ")
	if sepIdx == -1 {
		return ""
	}
	return rest[sepIdx:]
}

// RewriteAntigravityUserAgent replaces the version component of an Antigravity
// User-Agent while preserving both the "hub/" prefix (if present) and any
// platform suffix (e.g. " darwin/arm64").
// Parity with decolua/9router@6b8b7ceb9.
func RewriteAntigravityUserAgent(userAgent, version string) string {
	if userAgent == "" {
		return userAgent
	}
	if strings.HasPrefix(userAgent, antigravityUAHubPrefix) {
		return antigravityUAHubPrefix + version + trailingSuffix(userAgent, antigravityUAHubPrefix)
	}
	if strings.HasPrefix(userAgent, antigravityUABarePrefix) {
		return antigravityUABarePrefix + version + trailingSuffix(userAgent, antigravityUABarePrefix)
	}
	return userAgent
}

// HandleAntigravity intercepts Antigravity Gemini-native requests and forwards to 9router.
// It preserves client identity for catalog requests (fetchAvailableModels) and only
// overrides IDE version for generation endpoints (:generateContent/:streamGenerateContent)
// to keep compatibility while letting the IDE see new models (#3414).
func HandleAntigravity(w http.ResponseWriter, r *http.Request, body []byte) {
	// Decide whether to override IDE version based on request URL
	requestURL := r.URL.String()
	isGeneration := strings.Contains(requestURL, ":generateContent") || strings.Contains(requestURL, ":streamGenerateContent")

	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		SendError(w, http.StatusBadRequest, "invalid Antigravity request body")
		return
	}

	// Apply IDE version override only for generation endpoints
	if isGeneration {
		incomingUA := r.UserAgent()
		if incomingUA != "" {
			r.Header.Set("User-Agent", RewriteAntigravityUserAgent(incomingUA, antigravityIDEVersion))
		} else {
			r.Header.Set("User-Agent", "antigravity/"+antigravityIDEVersion)
		}
		// Override body metadata.ideVersion if present
		if meta, ok := reqBody["metadata"].(map[string]any); ok {
			meta["ideVersion"] = antigravityIDEVersion
			reqBody["metadata"] = meta
		} else if meta, ok := reqBody["request"].(map[string]any); ok {
			// Some payloads nest metadata inside request
			if innerMeta, ok := meta["metadata"].(map[string]any); ok {
				innerMeta["ideVersion"] = antigravityIDEVersion
			}
		}
	}

	model, _ := reqBody["model"].(string)
	if model != "" {
		reqBody["model"] = "antigravity/" + model
	}
	reqBody["userAgent"] = "antigravity"
	forwardBody, err := json.Marshal(reqBody)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "failed to marshal Antigravity request")
		return
	}

	isStream := len(r.URL.Query().Get("alt")) > 0 || strings.Contains(requestURL, ":streamGenerateContent") || strings.Contains(requestURL, ":generateContent")

	upstream, err := FetchRouter(r.Context(), forwardBody, "/v1/chat/completions", r.Header, "")
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
