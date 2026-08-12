# Design Spec: Default thought_signature Backfill for Gemini Sends

## 1. Overview

Add a safety net to the Gemini send path in `9router-go` so that **no `functionCall` part ever reaches a Gemini native endpoint without a `thoughtSignature`**. This mirrors the proven solution in the reference Next.js implementation (`/Users/luqmannul.hakim/htdocs/9router`), which hardcodes a default signature on every `functionCall` part and backfills one on any part that arrives unsigned. It closes the remaining 400-`thought_signature` gaps that turn-aware rotation (fix B) and the `__ts__` transport (fix A) cannot fully cover.

## 2. Background — where the remaining 400s come from

Gemini thinking models (e.g. `gemini-3.1-pro`, `gemini-3.5-flash`) reject any `functionCall` part in the **current turn** that lacks a `thoughtSignature`, returning:

```
400 Function call is missing a thought_signature in functionCall parts
```

A signature can only be produced by the Gemini model that made the call and echoed back through history. Two paths can still deliver an unsigned `functionCall` to Gemini:

1. **Mixed combos with mid-turn capability reorder** — `ReorderByCapabilities` runs on every request, including mid-turn. A `tool_result` carrying an image can float a Gemini model to the front mid-turn, and the current-turn function calls were made by a non-Gemini model (e.g. `deepseek`), so they carry no signature.
2. **Client drops the signature** — Claude Code (or another client) may not persist the `__ts__`-encoded tool-call id across a turn, losing the transport entirely.

The Next.js reference already solves both with a **default signature backfill**:
- `open-sse/translator/request/openai-to-gemini.js` sets `thoughtSignature: DEFAULT_THINKING_AG_SIGNATURE` on every `functionCall` part.
- `open-sse/executors/antigravity.js` backfills the default onto any `functionCall` part that arrives without one (comment: *"Clients (Claude Code, IDE) don't persist thoughtSignature in their history, so backfill the default signature on any functionCall part that arrives without one."*).

Provenance: this is a **known, already-addressed** gap in the reference, not an open bug. Go currently lacks the backfill.

## 3. Design

### 3.1 Constant

Add to `internal/translator/gemini.go`:

```go
// DefaultThinkingSignature is the hardcoded thought signature the Next.js
// reference backfills onto functionCall parts that arrive without one
// (DEFAULT_THINKING_AG_SIGNATURE). It keeps Gemini thinking models from
// rejecting a current-turn function call made by a non-Gemini model.
const DefaultThinkingSignature = "<DEFAULT_THINKING_AG_SIGNATURE blob>"
```

The blob value is copied verbatim from `htdocs/9router/open-sse/config/defaultThinkingSignature.js` (`DEFAULT_THINKING_AG_SIGNATURE`). Using the AG default for all Gemini-native sends matches the reference's `openai-to-gemini.js` default; per-endpoint defaults (Vertex, Gemini CLI) are deferred (YAGNI).

### 3.2 Sign every functionCall part

In `TranslateOpenAIToGemini`, in the `msg.ToolCalls` loop, change:

```go
			ts := extractThoughtSig(tc.ID)
			gp := GeminiPart{FunctionCall: &GeminiFunctionCall{Name: tc.Function.Name, Args: args}}
			if ts != "" {
				gp.ThoughtSignature = ts
			}
```

to:

```go
			ts := extractThoughtSig(tc.ID)
			if ts == "" {
				ts = DefaultThinkingSignature
			}
			gp := GeminiPart{FunctionCall: &GeminiFunctionCall{Name: tc.Function.Name, Args: args, ThoughtSignature: ts}}
```

Because the field is `omitempty`, every `functionCall` part now always carries `thoughtSignature`. **The real signature (via `__ts__` transport) is preferred; the default is used only when no real one exists.** No other part types are signed (the mandatory requirement is on function calls; reasoning-part signatures are optional and deferred).

## 4. Effect on output quality (documented trade-off)

- A signature is a **structural correlation token**, not reasoning content. Swapping it does not change the model's reasoning.
- Real signature = the producing model's own thinking trace; the default = a static generic value. In theory a real signature gives slightly more precise tool-output correlation in subsequent turns (the "degraded model performance" the API warning refers to). In practice the difference is negligible.
- **Full-Gemini combos are unaffected**: real signatures always exist (produced by the model, transported via `__ts__`, echoed by the client) plus turn-aware rotation keeps the same model in a turn. The default is effectively never exercised there.
- The only case that uses the default is a mixed-combo mid-turn switch or a client that dropped the signature — the exact cases that would otherwise 400.

## 5. Tests (`internal/translator/gemini_test.go`)

- `TestThoughtSignatureBackfill`: a request whose `tool_calls[].id` has **no** `__ts__` suffix → the emitted Gemini `functionCall` part has `ThoughtSignature == DefaultThinkingSignature`, and the raw body contains `"thoughtSignature":"` (camelCase), not `"thought_signature"`.
- Existing tests (round-trip with real signature, no-signature stream id cleanliness, camelCase regression) must still pass unchanged.

## 6. Out of Scope

- Per-endpoint default signatures (Vertex / Gemini CLI differ from AG).
- Signing reasoning (thought) parts.
- Removing or weakening fix A (`__ts__` transport) or fix B (turn-aware rotation) — the backfill complements both.

## 7. Acceptance Criteria

- `go test ./...` passes.
- Every `functionCall` part in an outbound Gemini-native request carries a `thoughtSignature` (real when available, default otherwise).
- The committed blob matches `DEFAULT_THINKING_AG_SIGNATURE` from the reference.