package executor

import (
	json "encoding/json/v2"
	"net/http"
	"strings"
)

// ForwardNvidia sanitizes the body NVIDIA NIM accepts before forwarding: the
// client params NIM rejects with 400, and the assistant reasoning alias that
// makes its deserializer report a duplicate field.
// Port of upstream open-sse/translator/concerns/paramSupport.js nvidia rule
// (PR #5247, hardened in decolua@2275bc0d).
func ForwardNvidia(w http.ResponseWriter, req *Request) error {
	if len(req.Body) > 0 {
		req.Body = sanitizeNIMBody(req.Body)
	}
	return ForwardOpenAI(w, req)
}

// nimRejectedParams are the top-level params NVIDIA NIM answers 400 for. NIM
// is OpenAI-compatible but not OpenAI: a client (Claude Code, the Codex CLI)
// sends its own metadata, cache key and tier alongside the prompt, and NIM's
// validator rejects every one of them instead of ignoring them.
var nimRejectedParams = []string{
	"client_metadata",
	"prompt_cache_key",
	"service_tier",
	"anthropic_beta",
	"thinking_budget",
	"user_id",
	"custom_field",
}

// sanitizeNIMBody drops the params NIM rejects and normalizes assistant
// reasoning aliases to reasoning_content. A body with nothing to change is
// returned byte for byte, so a clean request pays no re-serialisation.
func sanitizeNIMBody(raw []byte) []byte {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return raw
	}

	modified := false
	for _, key := range nimRejectedParams {
		if _, has := body[key]; has {
			delete(body, key)
			modified = true
		}
	}
	if normalizeNIMReasoning(body) {
		modified = true
	}

	if !modified {
		return raw
	}
	out, err := marshalStable(body)
	if err != nil {
		return raw
	}
	return out
}

// normalizeNIMReasoning renames an assistant turn's `reasoning` to
// `reasoning_content`, keeping an explicit canonical value when one is set.
// NIM treats the two as aliases of one field, so replaying both makes its
// deserializer fail on a duplicate field.
func normalizeNIMReasoning(body map[string]any) bool {
	msgs, ok := body["messages"].([]any)
	if !ok {
		return false
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
		if canonicalVal, hasCanonical := msg["reasoning_content"]; !hasCanonical || canonicalVal == nil || isWhitespaceString(canonicalVal) {
			msg["reasoning_content"] = aliasVal
		}
		delete(msg, "reasoning")
		modified = true
	}
	return modified
}

func isWhitespaceString(val any) bool {
	s, ok := val.(string)
	return ok && strings.TrimSpace(s) == ""
}
