package chat

import (
	"context"
	"encoding/json"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
)

// resolvePromptInjection returns the best matching injection document for a
// request, or nil when nothing should be injected. Specificity order is
// key-model > key > model > global. Only enabled injections are considered.
func (h *ChatHandler) resolvePromptInjection(modelID string, ctx context.Context) *db.PromptInjectionDoc {
	if h.Repo == nil {
		return nil
	}
	keyID := guardrailKeyIDFromContext(ctx)
	doc, err := h.Repo.ResolvePromptInjection(modelID, keyID)
	if err != nil {
		log.Warn("prompt-injection", "resolve failed", "model", modelID, "key", keyID, "error", err)
		return nil
	}
	if doc == nil || doc.Content == "" {
		return nil
	}
	return doc
}

// applyPromptInjection mutates the outbound request body for a request that
// resolves to an injection document. Called before the body is marshalled, so
// the prompt-cache hash always reflects the injected content. Two modes:
// "system" prepends a system prompt, "script" inserts a fake chat history.
func (h *ChatHandler) applyPromptInjection(ctx context.Context, body map[string]any, requestedModel string) context.Context {
	if h.Repo == nil {
		return ctx
	}
	settings, err := h.Repo.GetSettings()
	if err != nil || settings == nil || !settings.PromptInjectionEnabled {
		return ctx
	}
	doc := h.resolvePromptInjection(requestedModel, ctx)
	if doc == nil {
		return ctx
	}

	switch doc.Mode {
	case "script":
		applyScriptInjection(body, doc.Content)
	default:
		prependPromptInjection(body, doc.Content)
	}

	keyID := guardrailKeyIDFromContext(ctx)
	log.Info("prompt-injection", "applied", "model", requestedModel, "key", keyID, "mode", doc.Mode, "bytes", len(doc.Content))
	return ctx
}

// prependPromptInjection injects a system prompt into the request body,
// mirroring the existing prependSystemPrompt but for the Pro injection scope.
func prependPromptInjection(body map[string]any, prompt string) {
	if prompt == "" {
		return
	}
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

// applyScriptInjection inserts a fake chat history into the request body.
// Content is a JSON array of {role, content} turns.
func applyScriptInjection(body map[string]any, content string) {
	if content == "" {
		return
	}
	var turns []map[string]any
	if err := json.Unmarshal([]byte(content), &turns); err != nil {
		log.Warn("prompt-injection", "script parse failed", "error", err)
		return
	}
	if len(turns) == 0 {
		return
	}

	msgs, _ := body["messages"].([]any)
	scriptMsgs := make([]any, 0, len(turns))
	for _, turn := range turns {
		role, _ := turn["role"].(string)
		text, _ := turn["content"].(string)
		if role == "" || text == "" {
			continue
		}
		scriptMsgs = append(scriptMsgs, map[string]any{"role": role, "content": text})
	}
	if len(scriptMsgs) == 0 {
		return
	}
	body["messages"] = append(scriptMsgs, msgs...)
}
