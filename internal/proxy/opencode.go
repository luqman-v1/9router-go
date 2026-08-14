package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func toOpenCodeSession(id string) string {
	cleaned := strings.ReplaceAll(id, "-", "")
	cleaned = strings.TrimPrefix(cleaned, "ses_")
	if cleaned == "" {
		cleaned = randomHex(16)
	}
	return "ses_" + cleaned
}

// BuildOpenCodeHeaders generates the official OpenCode fingerprint headers to prevent 429 rate limits.
func BuildOpenCodeHeaders(rawHeaders map[string]string, sessionID string, isStream bool) map[string]string {
	res := map[string]string{
		"Content-Type":       "application/json",
		"Authorization":      "Bearer public",
		"User-Agent":         "opencode",
		"x-opencode-client":  "desktop",
		"x-opencode-session": toOpenCodeSession(sessionID),
		"x-opencode-request": "msg_" + randomHex(16),
		"x-opencode-project": "global",
	}
	if isStream {
		res["Accept"] = "text/event-stream"
	} else {
		res["Accept"] = "*/*"
	}
	for k, v := range rawHeaders {
		lk := strings.ToLower(k)
		if lk == "user-agent" && strings.Contains(strings.ToLower(v), "opencode") {
			res["User-Agent"] = v
		}
		if lk == "x-opencode-client" || lk == "x-opencode-project" || lk == "x-opencode-session" || lk == "x-opencode-request" {
			res[k] = v
		}
	}
	return res
}
