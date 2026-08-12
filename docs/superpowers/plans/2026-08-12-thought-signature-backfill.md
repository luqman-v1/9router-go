# Default thought_signature Backfill — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Guarantee every `functionCall` part in an outbound Gemini-native request carries a `thoughtSignature` — the real one via the `__ts__` transport when available, otherwise a hardcoded default — so Gemini thinking models never return the 400 `missing thought_signature` error.

**Architecture:** One constant (`DefaultThinkingSignature`, the Next.js `DEFAULT_THINKING_AG_SIGNATURE` blob) plus one change in `TranslateOpenAIToGemini`: sign each `functionCall` part, falling back to the default when `extractThoughtSig` yields nothing. Complements (does not replace) fix A (`__ts__` transport) and fix B (turn-aware rotation).

**Tech Stack:** Go, `go test`.

## Global Constraints

- Keep the real signature (via `__ts__`) preferred; the default is only a fallback.
- Do not sign non-`functionCall` parts (reasoning parts optional — out of scope).
- Emit the field as camelCase `thoughtSignature` (never `thought_signature`).
- `go test ./...` must pass at the commit.

---

### Task 1: Backfill default `thoughtSignature` on functionCall parts

**Files:**
- Modify: `internal/translator/gemini.go` (add const near `GeminiFunctionCall`; change the `msg.ToolCalls` loop in `TranslateOpenAIToGemini`)
- Test: `internal/translator/gemini_test.go`

**Interfaces:**
- Consumes: existing `extractThoughtSig(tc.ID) string` and `TranslateOpenAIToGemini(openaiBody []byte) ([]byte, error)`.
- Produces: exported `const DefaultThinkingSignature = "<blob>"`; `TranslateOpenAIToGemini` now emits `thoughtSignature` on every `functionCall` part.

- [ ] **Step 1: Add the constant**

In `internal/translator/gemini.go`, add above `type GeminiFunctionCall struct`:

```go
// DefaultThinkingSignature is the hardcoded thought signature the Next.js
// reference backfills onto functionCall parts that arrive without one
// (DEFAULT_THINKING_AG_SIGNATURE from open-sse/config/defaultThinkingSignature.js).
// Gemini thinking models reject a current-turn function call that carries no
// thought_signature; this keeps combos mixing Gemini with non-Gemini models
// from 400ing. The real signature (via __ts__ transport) is preferred whenever
// it exists.
const DefaultThinkingSignature = "EuwGCukGAXLI2nxwZIq54WWSoL/YN0P3TsDZ7zRnLi8g0S4aVr2HUGxvaHKySuY6HAVzcE0GPGjXrytLIldxthSvfxgUlJh6Qa9Z+Oj5QZBlYdg6HaJ6yuY5R7waE6rdwBsRf7Ft2j3DJ9rMi9qhWFqApewYtPhls3VHtuvND3l8Rm09+lbAXQs6KKWEWrxNLKTBkfpMgXhRERc/TQRMZu1twAablm6/Zk1tsYRvfWKLsNbeKF+CCojJdXJKvnR/8Ouuoa+Y2Ti20hcW7aZIIjZDFYPU//k6Ybmhg69J/imbFai2ckhfLaisqdDkdoIiBJScTOUvYqP6AE9d4MsydSC+UlhIMk4hoP76R8vUSCZRMkjOaDXstf/QoVZKbt94wyRZgAJ1G0BqI8L5ow86kLpA4wJEtxsRGymOE4bKUvApveBakYDNM9APkf+LbtbzWSseGjoZcSlycF9iN8Q2XNYKRrHbv3Lr5Y8JjdH/5y/6SHkNehTEZugaeGnSPSyCTWto1kQgHpxdWmhkLfJGNUGLmue7Mesj4TSms4J33mRpYVhNB/J333FCqIP0hr/E7BkkjEn7yZ4X7SQlh+xKPurapsnHRwiKmtsilmEFrnTE9iQr+pMr6M29qqFNv1tr5yumbaJw8JW9sB15tNsRv+dW6BjNanbsKz7HCgKUBc8tGy+7YuhXzAfViyRefcjK7eZW0Fbyt7AbybJTKz78W8NH7ye6LAwzOebXpeZ4D43fNIt8bKh26qgduSQv/7o+pAflkuqHZ99YWgHQ8h8OkZFi3eOiSYjsjhdZ/czWOdoPI/OnqIldzMPF5YlrKBLFX8VhRKVmqgsmWf5PHGulHhMkVlS+XG2UIseGy69ARa93D78Gsa+1n1kJr7EEB7Rh+27vUMxVYLdz1yMSvE5nalTAlg/ZeG8+XQ0cHuAI3KbQpHW2Q++RdXfm5JzD5WdJZUU+Zn8t8UUn85BH4RxZLeE0qJikgSsKoYVBc6YhiMjhPgkR95ReimY4Z0xCJdRo1gjexOFeODZMpQF6Yxnoic7IrdgsFA3iePTbFnPp3IAM1fAThWhXJUn3QInUOTd5o1qmTmn6REbL15g/JQNl+dqUoPkhleeb2V3kjqp1okmO3wMZbPknR3S1LZNmlS72/iBQUm+n2b/RCn4PjmM2"
```

- [ ] **Step 2: Write the failing test**

In `internal/translator/gemini_test.go`, add:

```go
func TestThoughtSignatureBackfill(t *testing.T) {
	// A function call with no __ts__ transport (made by a non-Gemini model, or
	// the client dropped the id) must still be signed with the default so
	// Gemini thinking models don't 400.
	openaiReq := `{
		"model": "gemini-3.5-flash",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": "", "tool_calls": [{"id": "call_plain_0", "type": "function", "function": {"name": "ScheduleWakeup", "arguments": "{\"time\":\"2026-08-13T00:00:00Z\"}"}}]}
		],
		"tools": [{"type": "function", "function": {"name": "ScheduleWakeup", "description": "wake", "parameters": {"type": "object", "properties": {"time": {"type": "string"}}}}]}]
	}`

	geminiBytes, err := TranslateOpenAIToGemini([]byte(openaiReq))
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
	}

	if !strings.Contains(string(geminiBytes), `"thoughtSignature":"`+DefaultThinkingSignature+`"`) {
		t.Errorf("unsigned functionCall must be backfilled with DefaultThinkingSignature, got: %s", geminiBytes)
	}
	if strings.Contains(string(geminiBytes), `"thought_signature"`) {
		t.Errorf("must emit camelCase thoughtSignature, got: %s", geminiBytes)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/translator -run TestThoughtSignatureBackfill -v`
Expected: FAIL — the emitted body has no `"thoughtSignature":` (backfill not implemented yet).

- [ ] **Step 4: Implement the backfill**

In `internal/translator/gemini.go`, inside `TranslateOpenAIToGemini`'s `msg.ToolCalls` loop, change:

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

- [ ] **Step 5: Run the translator tests**

Run: `go test ./internal/translator -v`
Expected: PASS — `TestThoughtSignatureBackfill` plus all existing tests (round-trip, stream, no-signature id cleanliness, camelCase regression).

- [ ] **Step 6: Run the full suite and commit**

```bash
go test ./...
git add internal/translator/gemini.go internal/translator/gemini_test.go
git commit -m "fix(translator): backfill default thought_signature on unsigned gemini functionCall parts"
```

---

## Self-Review Notes

- **Spec coverage:** §3.1 constant → Step 1; §3.2 signing → Step 4; §5 test → Step 2; §4 trade-off and §6 out-of-scope have no code, as intended. Acceptance criteria covered by Step 5 (all tests green) and the exact blob embedded in Step 1.
- **Placeholder scan:** no TBD; the constant carries the literal blob from `DEFAULT_THINKING_AG_SIGNATURE` (verified against the reference file).
- **Type consistency:** `DefaultThinkingSignature` is exported and referenced identically in Steps 1, 2, and 4; `extractThoughtSig`, `GeminiPart`, `GeminiFunctionCall`, and `TranslateOpenAIToGemini` use their existing signatures.
