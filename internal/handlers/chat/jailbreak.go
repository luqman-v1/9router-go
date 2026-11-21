package chat

import (
	"context"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
	"9router/proxy/internal/translator"
)

// resolveJailbreak returns the injection document for a request, or nil when
// nothing should be injected. ids are the candidate model ids in specificity
// order — the id the client sent first, then the ids the request is forwarded
// to — and the resolver applies exact bindings, keyword rules, and the
// exclude lists (see internal/db/jailbreak_keywords.go for the precedence).
// The authenticated client key scopes the lookup: a binding stored for that
// key outranks the same model's global binding. An empty document means
// pass-through, exactly like a toggle left off.
func (h *ChatHandler) resolveJailbreak(ctx context.Context, ids ...string) *db.JailbreakDoc {
	if h.Repo == nil {
		return nil
	}
	apiKeyID := ""
	if key := requestKeyFromContext(ctx); key != nil {
		apiKeyID = key.ID
	}
	doc, err := h.Repo.ResolveJailbreakDoc(apiKeyID, ids...)
	if err != nil {
		log.Warn("jailbreak", "doc lookup failed", "ids", ids, "error", err)
		return nil
	}
	if doc == nil || doc.Prompt == "" {
		return nil
	}
	return doc
}

// applyJailbreak mutates the outbound request body for a request that resolves
// to a document and returns a context carrying the Gemini-safety flag for the
// forward chain. Called before the body is marshalled, so the prompt-cache hash
// always reflects the injected prompt. Every forward path calls this: single
// chat, Claude single, combo fallback, Claude combo, fusion panel, fusion
// judge. requestedModel is the id the client sent — a model id, or the combo
// name, which arms every leg of that combo. legIDs are the ids the request is
// actually forwarded to, so a keyword such as "deepseek" covers a combo leg
// like providerx/deepseek-4-pro without arming that id.
func (h *ChatHandler) applyJailbreak(ctx context.Context, body map[string]any, requestedModel string, legIDs ...string) context.Context {
	doc := h.resolveJailbreak(ctx, append([]string{requestedModel}, legIDs...)...)
	if doc == nil {
		return ctx
	}
	injectJailbreakPrompt(body, doc)
	log.Info("jailbreak", "applied", "model", requestedModel,
		"prompt_bytes", len(doc.Prompt), "safety_off", doc.GeminiSafety)
	// Best-effort usage counter for the dashboard's per-preset hit count. A
	// failed write must not fail a request that is already being injected.
	if err := h.Repo.RecordJailbreakUsage(doc.ID); err != nil {
		log.Warn("jailbreak", "usage counter failed", "preset", doc.ID, "error", err)
	}
	if doc.GeminiSafety {
		ctx = translator.WithGeminiSafety(ctx, true)
	}
	return ctx
}

// jailbreakLegIDs returns the two ids a forwarded leg answers to: the
// "provider/model" form the dashboard and /v1/models use, and the bare model
// the upstream body carries. A keyword or exclude matches either.
func jailbreakLegIDs(provider, model string) []string {
	return []string{provider + "/" + model, model}
}

// Injectable positions and modes. "" means the legacy default and is what a
// preset written before these knobs existed carries; it must keep behaving
// exactly like "system", so a row never migrates on read.
const (
	jailbreakPositionSystem = "system"
	jailbreakPositionAppend = "append"
	jailbreakModeChat       = "chat"
)

// injectJailbreakPrompt is the single entry point for prompt injection: the
// ordering is mode first, position second, because a "chat" preset has no
// system prompt to prepend or append to and its Position is meaningless
// (enforced by the dashboard validator). Everything else falls through to
// prependSystemPrompt, whose bytes an empty doc must reproduce exactly.
func injectJailbreakPrompt(body map[string]any, doc *db.JailbreakDoc) {
	if doc.Mode == jailbreakModeChat {
		appendJailbreakUserTurn(body, doc.Prompt)
		return
	}
	if doc.Position == jailbreakPositionAppend {
		appendSystemPrompt(body, doc.Prompt)
		return
	}
	prependSystemPrompt(body, doc.Prompt)
}

// appendUserTurn appends the prompt as a trailing user turn in whatever
// conversation array the body already carries, whether it is OpenAI's
// "messages" or Claude's. A role:"system" message is never synthesized here:
// Anthropic rejects one, and the top-level "system" field is deliberately
// left untouched so an operator's own system prompt survives a chat-mode
// preset verbatim.
func appendJailbreakUserTurn(body map[string]any, prompt string) {
	msgs, _ := body["messages"].([]any)
	body["messages"] = append(msgs, map[string]any{"role": "user", "content": prompt})
}

// appendSystemPrompt puts the prompt after the original content instead of
// before it. Two wire formats reach here: OpenAI (system role inside
// "messages") and Claude (top-level "system", string or block array). The
// Claude path appends to the existing field rather than rewriting it, so a
// cache_control block on the operator's own system prompt stays put and only
// the prompt text grows. The OpenAI path appends a trailing system message:
// OpenAI accepts a system role anywhere in the array, while mutating a
// message already there could reorder or damage an operator's own turn.
func appendSystemPrompt(body map[string]any, prompt string) {
	if raw, ok := body["system"]; ok {
		switch v := raw.(type) {
		case string:
			body["system"] = v + "\n\n" + prompt
			return
		case []any:
			body["system"] = append(v, map[string]any{"type": "text", "text": prompt})
			return
		}
	}
	msgs, _ := body["messages"].([]any)
	body["messages"] = append(msgs, map[string]any{"role": "system", "content": prompt})
}

// prependSystemPrompt injects the configured prompt ahead of the original
// content. Two wire formats reach here: OpenAI (system role in "messages") and
// Claude (top-level "system", string or block array). Both get the prompt
// first, original content intact after it.
func prependSystemPrompt(body map[string]any, prompt string) {
	if raw, ok := body["system"]; ok {
		switch v := raw.(type) {
		case string:
			body["system"] = prompt + "\n\n" + v
			return
		case []any:
			body["system"] = append([]any{map[string]any{"type": "text", "text": prompt}}, v...)
			return
		}
	}
	msgs, _ := body["messages"].([]any)
	body["messages"] = append([]any{map[string]any{"role": "system", "content": prompt}}, msgs...)
}
