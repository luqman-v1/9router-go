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
// An empty document means pass-through, exactly like a toggle left off.
func (h *ChatHandler) resolveJailbreak(ids ...string) *db.JailbreakDoc {
	if h.Repo == nil {
		return nil
	}
	doc, err := h.Repo.ResolveJailbreakDoc(ids...)
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
	doc := h.resolveJailbreak(append([]string{requestedModel}, legIDs...)...)
	if doc == nil {
		return ctx
	}
	prependSystemPrompt(body, doc.Prompt)
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
