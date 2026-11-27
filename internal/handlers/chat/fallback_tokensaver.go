package chat

import (
	"9router/proxy/internal/headroom"
	"9router/proxy/internal/log"
	"9router/proxy/internal/tokensaver"
	"9router/proxy/internal/translator"
	"context"
	"fmt"
	"strings"
)

// claudeCacheTTL is the operator's head-anchor prompt-cache window. It reads
// through TokenSaver because that config is already the per-request settings
// snapshot the chat handler refreshes after every dashboard save.
func (h *ChatHandler) claudeCacheTTL() string {
	if h.TokenSaver == nil {
		return translator.ClaudeCacheTTL5m
	}
	return h.TokenSaver.ClaudeCacheTTL()
}

func (h *ChatHandler) applyTokenSavers(ctx context.Context, body []byte, claudeNative bool) ([]byte, int, int, int) {
	// X-9Router-Token-Saver: off|0|false|bypass opts the whole request out of
	// every token saver — RTK, Caveman, Ponytail, ADHD, Headroom, system prompt
	// prefix/suffix — and the body is forwarded untouched with 0 tokens saved.
	if TokenSaverBypassEnabled(ctx) {
		return body, 0, 0, 0
	}
	if h.TokenSaver == nil {
		return body, 0, 0, 0
	}
	// Prompt-injection guard: tag (never block) flagged user content. Early
	// detection here means operators can see abuse before it reaches upstream.
	// Toggle via settings.injectionGuardEnabled (off bypasses the scan).
	if h.TokenSaver.InjectionGuardEnabled() {
		if inj := tokensaver.DetectInjection(body); inj.Flagged {
			log.Warn("guard", "prompt injection flagged", "reasons", inj.Reasons, "messageId", inj.MessageID)
		}
	}
	out := body
	var origTokens, savedTokens, savedPct int
	if h.TokenSaver.ReaAllowEnabled() {
		format := detectReaFormat(out, claudeNative)
		if next, did := tokensaver.ApplyReaAllow(out, format); did {
			out = next
		}
	}
	if h.TokenSaver.RTKEnabled() {
		rtkCfg := h.TokenSaver.RTKConfig()
		if next, did := tokensaver.CompressMessagesWithConfig(out, rtkCfg); did {
			origTokens = len(out) / 4
			compressedTokens := len(next) / 4
			savedTokens = origTokens - compressedTokens
			savedPct = 0
			if origTokens > 0 {
				savedPct = (savedTokens * 100) / origTokens
			}
			log.Info(
				"token_saver", "RTK compressed tool output",
				"orig_est", origTokens,
				"compressed_est", compressedTokens,
				"saved_est", savedTokens,
				"saved_pct", fmt.Sprintf("%d%%", savedPct),
			)
			out = next
		}
	}
	if h.TokenSaver.CavemanInputMode() {
		if next, did := tokensaver.CompressInputMessages(out, h.TokenSaver.CavemanPreserveKeywords()); did {
			out = next
		}
	}
	inject := tokensaver.InjectSystemPrompt
	if claudeNative {
		inject = tokensaver.InjectSystemPromptClaude
	}
	// Persona prompts only ever add to the body, so the size before them is the
	// baseline each injection is measured against. The baseline moves after
	// every successful injection: a persona added on top of an earlier one adds
	// only its own prompt, and measuring it against the pre-persona length would
	// count the earlier persona's bytes a second time.
	beforeLen := len(out)
	if h.TokenSaver.CavemanEnabled() {
		bypass := false
		if h.TokenSaver.CavemanAutoClarity() && tokensaver.ShouldBypassCaveman(out) {
			bypass = true
		}
		if !bypass {
			prompt := tokensaver.GetCavemanPromptWithLang(h.TokenSaver.CavemanLevel(), h.TokenSaver.CavemanLanguage())
			if next, did := inject(out, prompt); did {
				out = next
				recordPersonaInjection(&origTokens, &savedTokens, beforeLen, len(next))
				beforeLen = len(out)
			}
		}
	}
	if h.TokenSaver.PonytailEnabled() {
		prompt := tokensaver.GetPonytailPrompt(h.TokenSaver.PonytailLevel())
		if next, did := inject(out, prompt); did {
			out = next
			recordPersonaInjection(&origTokens, &savedTokens, beforeLen, len(next))
			beforeLen = len(out)
		}
	}
	if h.TokenSaver.ADHDEnabled() {
		prompt := tokensaver.GetADHDPrompt(h.TokenSaver.ADHDLevel())
		if next, did := inject(out, prompt); did {
			out = next
			recordPersonaInjection(&origTokens, &savedTokens, beforeLen, len(next))
			// No baseline update: this is the last persona, so nothing reads
			// beforeLen again. A persona added after this one must move the
			// baseline here too, or it will count the prompts above it twice.
		}
	}
	// Headroom runs after the in-process savers and before the global prompt, so
	// it never sees the prompt this process is about to add.
	//
	// The client is built per call rather than cached on the handler: the engine
	// and the proxy URL are operator settings that can change while requests are
	// in flight, and a shared client would need a lock around every field read
	// to stay correct. Compress itself fails open on every refusal, so the only
	// cost of not caching is one struct allocation.
	//
	// The token counts are measured here rather than taken from the engine: the
	// same estimator RTK uses above, so a saving from either stage is comparable
	// on one scale and the usage page shows one number instead of two. Without
	// this, headroom-only compression left SavedTokens at 0, and the usage page
	// reported no compression at all on a request that was demonstrably
	// compressed — the analytics row is only written when SavedTokens > 0.
	if h.TokenSaver.HeadroomEnabled() {
		url, timeoutMs := h.TokenSaver.HeadroomEndpoint()
		engine := h.TokenSaver.HeadroomEngineName()
		// The built-in engine needs no URL, so a compression that is switched on
		// must not silently do nothing because the URL field happens to be empty.
		if url != "" || headroom.NormalizeEngine(engine) == headroom.EngineBuiltin {
			client := headroom.NewClientWithEngine(url, "", nil, engine)
			if next, did, err := client.Compress(ctx, out, "", timeoutMs); err == nil && did {
				nextOrig := len(out) / 4
				nextCompressed := len(next) / 4
				nextSaved := nextOrig - nextCompressed
				if nextSaved > 0 {
					// Headroom runs after RTK, so its saving is on top of the one
					// already measured: the totals are cumulative, matching what
					// the provider is about to be billed for.
					if nextOrig > origTokens {
						origTokens = nextOrig
					}
					savedTokens += nextSaved
					if origTokens > 0 {
						savedPct = (savedTokens * 100) / origTokens
					}
					log.Info(
						"token_saver", "headroom compressed request",
						"engine", headroom.NormalizeEngine(engine),
						"orig_est", nextOrig,
						"compressed_est", nextCompressed,
						"saved_est", nextSaved,
						"total_saved_est", savedTokens,
						"saved_pct", fmt.Sprintf("%d%%", savedPct),
					)
				}
				out = next
			}
		}
	}
	// Global system prompt, last in the chain so the prefix stays the first
	// thing the model reads and the suffix its last word — after every style
	// prompt has been appended in between. Skipped when a combo's own custom
	// prompt was already injected (the combo prompt replaces the global one).
	if h.TokenSaver.SystemPromptEnabled() && !ComboCustomPromptInjected(ctx) {
		if prefix := h.TokenSaver.SystemPromptPrefix(); prefix != "" {
			if next, did := tokensaver.InjectPrefixSystemPrompt(out, prefix, claudeNative); did {
				out = next
			}
		}
		if suffix := h.TokenSaver.SystemPromptSuffix(); suffix != "" {
			if next, did := tokensaver.InjectSuffixSystemPrompt(out, suffix, claudeNative); did {
				out = next
			}
		}
	}
	return out, origTokens, savedTokens, savedPct
}

// tokenSaverBypassKey carries the X-9Router-Token-Saver opt-out on the context.
type tokenSaverBypassKey struct{}

// WithTokenSaverBypass marks ctx as a token-saver bypass: no compression or
// prompt injection may run on this request.
func WithTokenSaverBypass(ctx context.Context) context.Context {
	return context.WithValue(ctx, tokenSaverBypassKey{}, true)
}

// TokenSaverBypassEnabled reports whether the request opted out of token saving.
func TokenSaverBypassEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, ok := ctx.Value(tokenSaverBypassKey{}).(bool)
	return ok && v
}

// comboCustomPromptKey carries the fact that a combo's own custom system prompt
// was already injected into the body, so the global system prompt prefix/suffix
// must not be added on top of it.
type comboCustomPromptKey struct{}

// WithComboCustomPrompt marks ctx as having received the combo's custom system
// prompt, so applyTokenSavers skips the global system prompt injection.
func WithComboCustomPrompt(ctx context.Context) context.Context {
	return context.WithValue(ctx, comboCustomPromptKey{}, true)
}

// ComboCustomPromptInjected reports whether a combo custom prompt was already
// injected into the request body.
func ComboCustomPromptInjected(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, ok := ctx.Value(comboCustomPromptKey{}).(bool)
	return ok && v
}

// isTokenSaverBypassValue reports whether an X-9Router-Token-Saver header value
// opts the request out of every token saver (false, 0, off, bypass,
// case-insensitive).
func isTokenSaverBypassValue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "0", "off", "bypass":
		return true
	default:
		return false
	}
}
