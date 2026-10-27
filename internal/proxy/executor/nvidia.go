package executor

import (
	json "encoding/json/v2"
	"net/http"
	"strings"
)

// ForwardNvidia normalizes assistant reasoning aliases before forwarding
// to NVIDIA NIM, avoiding NIM's duplicate-field 400 when clients send both.
// Port of upstream open-sse/translator/concerns/paramSupport.js (PR #5247).
func ForwardNvidia(w http.ResponseWriter, req *Request) error {
	if len(req.Body) > 0 {
		req.Body = normalizeNvidiaReasoning(req.Body)
	}
	return ForwardOpenAI(w, req)
}

// normalizeNvidiaReasoning normalizes assistant-message `reasoning` aliases
// to `reasoning_content` and removes `reasoning` to prevent duplicate-field
// deserialization errors in NVIDIA NIM.
func normalizeNvidiaReasoning(raw []byte) []byte {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return raw
	}

	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return raw
	}

	modified := false
	for _, item := range msgs {
		msg, ok := item.(map[string]any)
		if !ok || msg == nil {
			continue
		}
		if role, _ := msg["role"].(string); role != "assistant" {
			continue
		}

		aliasVal, hasAlias := msg["reasoning"]
		if !hasAlias {
			continue
		}

		canonicalVal, hasCanonical := msg["reasoning_content"]
		canonicalEmpty := !hasCanonical || canonicalVal == nil || (isWhitespaceString(canonicalVal))

		if canonicalEmpty {
			msg["reasoning_content"] = aliasVal
		}
		delete(msg, "reasoning")
		modified = true
	}

	if !modified {
		return raw
	}

	out, err := json.Marshal(body)
	if err != nil {
		return raw
	}
	return out
}

func isWhitespaceString(val any) bool {
	s, ok := val.(string)
	return ok && strings.TrimSpace(s) == ""
}
