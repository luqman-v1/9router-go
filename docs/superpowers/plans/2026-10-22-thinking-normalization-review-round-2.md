# Thinking Normalization Review Round 2 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the 4 critical, 8 major and 16 minor findings from the round-2 review of `fix/thinking-normalization` against `upstream/main`.

**Architecture:** Four behavioural fixes (translator determinism, dialect resolution, byte-identity on the decode path, operator-override dialect) plus one resolution-order fix, then coverage and hygiene. Every behavioural fix lands with a failing test first. The byte-identity work follows the pattern `FilterToOpenAIFormatJSON` already uses in this branch: report whether the pass changed anything, and return the input bytes when it did not.

**Implementation notes (added after the work, so the plan matches the tree):** the suffix intent travels on the request context (`internal/thinking/context.go`) *and* is read from the body's own `model` field (`thinking.ModelSuffixIntent`), because the combo path replaces that field with the member it is about to call. `thinking.Apply` and `thinking.Strip` return a change report; `thinking.ApplyClaudeThinkingOverride` (`internal/thinking/claude.go`) writes the Claude dialect for a Claude client.

**Tech Stack:** Go 1.27, `encoding/json/v2` (`json.Deterministic(true)`), standard-library `testing`, `httptest`, the `integration` build tag.

**Spec:** `docs/superpowers/plans/2026-10-21-thinking-normalization-review-followups.md` (round-2 findings are recorded in the review report of this session; the branch's own contract lives in `.changes/1005-thinking-normalization-parity.md` and `.changes/531-thinking-normalization-review-fixes.md`).

## Global Constraints

- Base is `upstream/main` (`7669175`), not local `main` (`6b01745`, 39 commits stale).
- `CHANGELOG.md` must not be touched (§7.6). Changelog entries go in `.changes/<pr-number>-<slug>.md`.
- Every `map[string]any` → `[]byte` wire boundary must marshal with `json.Deterministic(true)`.
- A body the thinking pass does not change must be returned **byte for byte**, including integers above 2^53.
- Go idioms: wrap errors with `%w`, no `panic`, comma-ok type assertions, files ≤ 300 LoC, cognitive complexity ≤ 15.
- Tests use `testing.T` only; no sleeps, no polling, no map-order assertions; assert machine-consumed values (parsed fields, exact wire bytes).
- Integration tests live under `internal/integration/` with the `//go:build integration` tag and run via `make test-integration`.

## Review Focus

The input classes the spec implies but no existing test exercises — each gets its test in the owning task:

1. **A Claude client sends `tool_choice: {"type":"tool","name":"x"}`** — the upstream must receive byte-identical bodies across repeated identical requests (Task 1).
2. **A request body contains the word "reasoning" inside a user message** — the body must still reach the upstream byte for byte; the fast path must not be defeated by content (Task 2).
3. **A request body carries an integer above 2^53 (`user_id: 9007199254740993`)** — the upstream must receive the same integer, not `…992` (Task 3).
4. **A Claude Messages client targets a Messages-speaking upstream while an operator `providerThinking` override is set** — the upstream must not receive an OpenAI-only `reasoning_effort` field (Task 4).
5. **A client asks for `mycombo(high)` — an alias or combo name with a `(level)` suffix** — the request must resolve to the combo, not 400 with "could not resolve model" (Task 4).

---

### Task 1: Translator determinism and tool normalisation

**Files:**
- Modify: `internal/translator/request.go:471` (the `TranslateClaudeToOpenAI` final marshal)
- Modify: `internal/translator/openai_filter.go:44-66` (`filterToOpenAIFormat`)
- Modify: `internal/translator/request_test.go:337-355` (`TestBudgetToEffort`)
- Create: `internal/translator/openai_filter_determinism_test.go` (extend)

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `translator.FilterToOpenAIFormatJSON(body []byte, opts FilterOptions) []byte` — unchanged signature; behaviour change is that `{"tools":[]}` without a `messages` key now has `tools` deleted.

- [ ] **Step 1: Write the failing test — the Claude tool_choice marshal is deterministic**

In `internal/translator/openai_filter_determinism_test.go`:

```go
func TestTranslateClaudeToOpenAIToolChoiceIsDeterministic(t *testing.T) {
	body := []byte(`{"model":"claude-x","max_tokens":100,"tool_choice":{"type":"tool","name":"f"},"messages":[{"role":"user","content":"hi"}]}`)
	first, err := TranslateClaudeToOpenAI(body)
	if err != nil {
		t.Fatalf("TranslateClaudeToOpenAI: %v", err)
	}
	for i := 0; i < 200; i++ {
		got, err := TranslateClaudeToOpenAI(body)
		if err != nil {
			t.Fatalf("TranslateClaudeToOpenAI: %v", err)
		}
		if !bytes.Equal(got, first) {
			t.Fatalf("the translated body is not stable:\nfirst: %s\ngot:   %s", first, got)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/translator/ -run TestTranslateClaudeToOpenAIToolChoiceIsDeterministic -v`
Expected: FAIL — the two `tool_choice` key orderings alternate.

- [ ] **Step 3: Add `json.Deterministic(true)` to the final marshal**

In `internal/translator/request.go`, change:

```go
out, err := json.Marshal(oreq)
```

to:

```go
out, err := json.Marshal(oreq, json.Deterministic(true))
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/translator/ -run TestTranslateClaudeToOpenAIToolChoiceIsDeterministic -v`
Expected: PASS

- [ ] **Step 5: Write the failing test — the determinism test must exercise the marshal, not the passthrough**

Replace the body used by `TestFilterToOpenAIFormatJSONDeterministic` in `internal/translator/openai_filter_determinism_test.go` with one the filter actually rewrites:

```go
body := []byte(`{"model":"m","messages":[{"role":"developer","content":"x"},` +
	`{"role":"user","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"a"}]}],` +
	`"tool_choice":{"type":"any"}}`)
```

and add an assertion that the filter changed the body, so the test cannot pass through the passthrough:

```go
if bytes.Equal(out, body) {
	t.Fatalf("the fixture must be rewritten by the filter, otherwise the determinism assertion is vacuous")
}
```

- [ ] **Step 6: Write the failing test — tools normalisation does not require a messages key**

```go
func TestFilterToOpenAIFormatJSONNormalizesToolsWithoutMessages(t *testing.T) {
	out := FilterToOpenAIFormatJSON([]byte(`{"tools":[]}`), FilterOptions{})
	if strings.Contains(string(out), "tools") {
		t.Fatalf("an empty tool list must be dropped, got %s", out)
	}
	out = FilterToOpenAIFormatJSON([]byte(`{"tool_choice":{"type":"any"}}`), FilterOptions{})
	if !strings.Contains(string(out), `"required"`) {
		t.Fatalf("a Claude-shaped tool_choice must be normalised without a messages key, got %s", out)
	}
}
```

- [ ] **Step 7: Run test to verify it fails**

Run: `go test ./internal/translator/ -run TestFilterToOpenAIFormatJSONNormalizesToolsWithoutMessages -v`
Expected: FAIL — the early `return false` skips `normalizeTools` / `normalizeToolChoice`.

- [ ] **Step 8: Run the tools and tool_choice passes regardless of the messages key**

In `internal/translator/openai_filter.go`, replace the body of `filterToOpenAIFormat` with:

```go
func filterToOpenAIFormat(body map[string]any, opts FilterOptions) bool {
	changed := false
	if msgs, ok := body["messages"].([]any); ok {
		if filtered, messagesChanged := filterMessages(msgs, opts); messagesChanged {
			body["messages"] = filtered
			changed = true
		}
	}
	if normalizeTools(body) {
		changed = true
	}
	if normalizeToolChoice(body) {
		changed = true
	}
	return changed
}
```

- [ ] **Step 9: Run the translator suite**

Run: `go test ./internal/translator/`
Expected: PASS

- [ ] **Step 10: Fix the coercing `TestBudgetToEffort`**

In `internal/translator/request_test.go`, delete the coercion and expect the real contract. The function under test is `thinking.BudgetToLevel`, whose contract is `0 → ""` (pinned by `internal/thinking/levels_test.go`). Change the table row for budget `0` to expect `""`, delete the `if got == "" { got = "medium" }` line, and rename the test to `TestBudgetToLevel`.

- [ ] **Step 11: Run the translator suite again**

Run: `go test ./internal/translator/`
Expected: PASS

- [ ] **Step 12: Commit**

```bash
git add internal/translator/request.go internal/translator/openai_filter.go internal/translator/openai_filter_determinism_test.go internal/translator/request_test.go
git commit -m "fix(translator): marshal the Claude translation deterministically and normalise tools without a messages key"
```

---

### Task 2: Dialect resolution, the tokenrouter clamp, and the fast-path scan

**Files:**
- Modify: `internal/thinking/format.go:181-207` (`resolveFormat`)
- Modify: `internal/thinking/writers_table.go:226-236` (`writeTokenRouter`)
- Modify: `internal/thinking/suffix.go:126-150` (`SnapshotKeys`, `BodyMentionsThinking`)
- Test: `internal/thinking/format_test.go`, `internal/thinking/format_writers_test.go`, `internal/thinking/scan_test.go`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `thinking.BodyMentionsThinking(raw []byte) bool` — unchanged signature; behaviour change is that a thinking key must be a JSON key, not a substring.

- [ ] **Step 1: Write the failing test — the Responses dialect wins over the provider registry**

In `internal/thinking/format_test.go`:

```go
func TestResolveFormatPrefersTheWireOverTheProviderRegistry(t *testing.T) {
	out := apply(t, "openai-responses", "openrouter", "openai/gpt-5.4",
		map[string]any{"model": "openai/gpt-5.4", "reasoning_effort": "high"}, nil)
	r, _ := out["reasoning"].(map[string]any)
	if r == nil || r["effort"] != "high" {
		t.Fatalf("a Responses wire must use the Responses dialect, got %+v", out)
	}
	if _, has := out["reasoning_effort"]; has {
		t.Fatalf("a Responses wire must not carry a top-level reasoning_effort, got %+v", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/thinking/ -run TestResolveFormatPrefersTheWireOverTheProviderRegistry -v`
Expected: FAIL — `resolveFormat` returns the registry's `"openai"` first, so the body carries `reasoning_effort`.

- [ ] **Step 3: Consult the wire before the provider registry**

In `internal/thinking/format.go`, replace `resolveFormat` with:

```go
func resolveFormat(target, provider, model string, caps providers.Capabilities) string {
	if target == "commandcode" {
		return "commandcode"
	}
	// The wire is authoritative: a Responses upstream rejects a top-level
	// reasoning_effort ("unknown parameter `reasoning_effort`") and reads
	// reasoning:{effort,summary} instead. The dialect follows the wire, not the
	// provider registry, so every provider on that lane gets it — including the
	// nine aggregators the registry records as "openai".
	if target == "openai-responses" && !nativeOnlyFormats[caps.ThinkingFormat] {
		return "openai-responses"
	}
	if provider != "" {
		if pf := providers.ThinkingFormatFor(provider); pf != "" {
			return pf
		}
	}
	isOpenAIWire := target == "openai" || target == "openai-responses"
	if caps.ThinkingFormat != "" && (!isOpenAIWire || !nativeOnlyFormats[caps.ThinkingFormat]) {
		return caps.ThinkingFormat
	}
	if f, ok := formatToNative[target]; ok {
		return f
	}
	return "openai"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/thinking/ -run TestResolveFormatPrefersTheWireOverTheProviderRegistry -v`
Expected: PASS

- [ ] **Step 5: Run the whole thinking suite**

Run: `go test ./internal/thinking/`
Expected: PASS

- [ ] **Step 6: Write the failing test — the tokenrouter dialect honours the none clamp**

In `internal/thinking/format_writers_test.go`:

```go
func TestApplyTokenRouterClampsDisableOnAModelThatCannotDisable(t *testing.T) {
	out := apply(t, "openai", "tokenrouter", "z-ai/glm-5.3-free",
		map[string]any{"reasoning_effort": "none"}, nil)
	if out["reasoning_effort"] == nil {
		t.Fatalf("a model that cannot disable must receive the clamped level, got %+v", out)
	}
}
```

- [ ] **Step 7: Run test to verify it fails**

Run: `go test ./internal/thinking/ -run TestApplyTokenRouterClampsDisableOnAModelThatCannotDisable -v`
Expected: FAIL — `writeTokenRouter` returns early on `c.none` and writes nothing.

- [ ] **Step 8: Write the clamped effort in the tokenrouter dialect**

In `internal/thinking/writers_table.go`, replace `writeTokenRouter` with:

```go
func writeTokenRouter(c *writerCtx) {
	if c.none && c.canDisable {
		return
	}
	if c.eff.Mode == ModeAuto {
		return
	}
	if lvl := ToLevel(c.eff); lvl != "" {
		c.body["reasoning_effort"] = knownLevel(lvl)
	}
}
```

- [ ] **Step 9: Run test to verify it passes**

Run: `go test ./internal/thinking/ -run TestApplyTokenRouterClampsDisableOnAModelThatCannotDisable -v`
Expected: PASS

- [ ] **Step 10: Run the whole thinking suite**

Run: `go test ./internal/thinking/`
Expected: PASS — `TestApplyTokenRouter` still passes, because `step-5-preview` can disable and the `none` case emits nothing.

- [ ] **Step 11: Write the failing test — the fast path survives a thinking word in the content**

In `internal/thinking/scan_test.go`:

```go
func TestBodyMentionsThinkingIgnoresContent(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"explain \"reasoning\" please"}]}`)
	if BodyMentionsThinking(body) {
		t.Fatalf("a thinking word in the content is not a thinking field: %s", body)
	}
	body = []byte(`{"model":"m","reasoning_effort":"high"}`)
	if !BodyMentionsThinking(body) {
		t.Fatalf("a thinking key must be detected: %s", body)
	}
}
```

- [ ] **Step 12: Run test to verify it fails**

Run: `go test ./internal/thinking/ -run TestBodyMentionsThinkingIgnoresContent -v`
Expected: FAIL — the substring scan matches the content.

- [ ] **Step 13: Match a JSON key, not a substring**

In `internal/thinking/suffix.go`, replace `BodyMentionsThinking` with:

```go
func BodyMentionsThinking(raw []byte) bool {
	for _, k := range snapshotKeyBytes {
		for off := 0; off < len(raw); {
			rel := bytes.Index(raw[off:], k)
			if rel == -1 {
				break
			}
			i := off + rel + len(k)
			off = i
			i = skipJSONSpace(raw, i)
			if i < len(raw) && raw[i] == ':' {
				return true
			}
		}
	}
	return false
}
```

`skipJSONSpace` already exists in `internal/thinking/format.go` and skips whitespace and the colon; for this scan the colon must be *after* the key, so use a whitespace-only skip. Define a local helper in `suffix.go`:

```go
func skipKeySpace(raw []byte, i int) int {
	for i < len(raw) {
		switch raw[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}
```

and use `skipKeySpace` in the loop above.

- [ ] **Step 14: Run test to verify it passes**

Run: `go test ./internal/thinking/ -run TestBodyMentionsThinkingIgnoresContent -v`
Expected: PASS

- [ ] **Step 15: Run the whole thinking suite**

Run: `go test ./internal/thinking/`
Expected: PASS

- [ ] **Step 16: Commit**

```bash
git add internal/thinking/format.go internal/thinking/writers_table.go internal/thinking/suffix.go internal/thinking/format_test.go internal/thinking/format_writers_test.go internal/thinking/scan_test.go
git commit -m "fix(thinking): let the wire pick the dialect, clamp the tokenrouter disable, and scan for thinking keys"
```

---

### Task 3: Byte-identity on the decode path

**Files:**
- Modify: `internal/thinking/format.go:41-133` (`Apply`, `ApplyJSON`)
- Modify: `internal/translator/content_strip.go:9-60` (`StripContentTypes`)
- Modify: `internal/handlers/chat/thinking_wire.go:18-46` (`prepareInboundThinking`)
- Test: `internal/thinking/format_test.go`, `internal/translator/content_strip_test.go` (create), `internal/handlers/chat/thinking_wire_test.go`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces:
  - `thinking.Apply(body map[string]any, target, provider, model string, override *Intent) (map[string]any, bool)` — the bool reports whether the body changed. **Signature change**; Task 4 calls it.
  - `translator.StripContentTypes(body map[string]any, strip []string) bool` — the bool reports whether the body changed. **Signature change**; the only caller is `prepareInboundThinking`.

- [ ] **Step 1: Write the failing test — a body the pass does not change keeps its bytes**

In `internal/thinking/format_test.go`:

```go
func TestApplyJSONLeavesAnUnchangedBodyByteIdentical(t *testing.T) {
	body := []byte(`{"model":"m","user_id":9007199254740993,"messages":[{"role":"user","content":"hi"}]}`)
	out := ApplyJSON(body, "openai", "openai", "m", nil)
	if !bytes.Equal(out, body) {
		t.Fatalf("an unchanged body must be returned byte for byte:\nin:  %s\nout: %s", body, out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/thinking/ -run TestApplyJSONLeavesAnUnchangedBodyByteIdentical -v`
Expected: FAIL today only if the fast path is defeated; the second case below is the one that fails.

- [ ] **Step 3: Write the failing test — an override that changes nothing still keeps the bytes**

```go
func TestApplyJSONKeepsTheBytesWhenTheOverrideChangesNothing(t *testing.T) {
	body := []byte(`{"model":"m","user_id":9007199254740993,"reasoning_effort":"low","messages":[{"role":"user","content":"hi"}]}`)
	out := ApplyJSON(body, "openai", "openai", "m", &Intent{Mode: ModeLevel, Level: "high"})
	if !bytes.Equal(out, body) {
		t.Fatalf("a body the pass does not change must keep its bytes:\nin:  %s\nout: %s", body, out)
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `go test ./internal/thinking/ -run TestApplyJSONKeepsTheBytesWhenTheOverrideChangesNothing -v`
Expected: FAIL — the body is re-marshalled, the keys are reordered and `9007199254740993` becomes `…992`.

- [ ] **Step 5: Report whether `Apply` changed anything**

In `internal/thinking/format.go`, change `Apply` to return `(map[string]any, bool)`:

```go
func Apply(body map[string]any, target, provider, model string, override *Intent) (map[string]any, bool) {
	if len(body) == 0 {
		return body, false
	}
	clean, suffix := ParseSuffix(model)
	if m, _ := body["model"].(string); m != "" {
		if stripped := StripSuffix(m); stripped != m {
			body["model"] = stripped
		}
	}
	cfg := override
	if cfg == nil {
		cfg = suffix
	}
	caps := providers.GetCapabilitiesForModel(provider, clean)
	if !caps.Reasoning {
		return body, Strip(body)
	}
	if cfg == nil {
		cfg = Extract(body)
	}
	if cfg == nil {
		return body, false
	}
	fmt := resolveFormat(target, provider, clean, caps)
	if !hasWriter(fmt) {
		return body, false
	}
	var display string
	if t, ok := body["thinking"].(map[string]any); ok {
		display, _ = t["display"].(string)
	}
	if display == "" && override != nil {
		display = override.Display
	}
	changed := Strip(body)
	applyFormat(fmt, body, cfg, caps, providers.GetThinkingLevels(provider, clean), display)
	return body, true || changed
}
```

`Strip` must report whether it removed anything — change its signature in `internal/thinking/suffix.go` to `func Strip(body map[string]any) bool` and return `true` when at least one key was deleted.

- [ ] **Step 6: Return the input bytes when the pass changed nothing**

In `internal/thinking/format.go`, replace `ApplyJSON`:

```go
func ApplyJSON(body []byte, target, provider, model string, override *Intent) []byte {
	if shouldSkipNormalization(body, model, override) {
		return body
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body
	}
	if _, changed := Apply(m, target, provider, model, override); !changed {
		return body
	}
	out, err := json.Marshal(m, json.Deterministic(true))
	if err != nil {
		return body
	}
	return out
}
```

- [ ] **Step 7: Update the callers of `Apply` and `Strip`**

`internal/thinking/format.go` `Apply` is the only production caller of `Strip` besides `Apply` itself. `internal/handlers/chat/thinking_wire.go` calls `translator.StripContentTypes`; update after Step 9.

- [ ] **Step 8: Run the thinking suite**

Run: `go test ./internal/thinking/`
Expected: PASS — `TestApplyJSON` "Malformed: unchanged" still holds, because a decode error returns the input.

- [ ] **Step 9: Report whether `StripContentTypes` changed anything**

In `internal/translator/content_strip.go`, change the signature to `func StripContentTypes(body map[string]any, strip []string) bool` and return `true` when a block was removed or an emptied content array was replaced by `""`. Every early return becomes `return false`.

- [ ] **Step 10: Keep the inbound body byte-identical when the inbound pass changes nothing**

In `internal/handlers/chat/thinking_wire.go`, rewrite `prepareInboundThinking` so it tracks whether anything changed and returns the input bytes when nothing did:

```go
func (h *ChatHandler) prepareInboundThinking(provider, model string, body []byte) ([]byte, string, *thinking.Intent) {
	wireModel, suffixIntent := thinking.ParseSuffix(model)
	needsWire := wireModel != model || suffixIntent != nil
	mode := h.providerThinkingMode(provider)
	if !needsWire && len(providers.ModelStrip(provider, model)) == 0 &&
		mode == "" && !thinking.BodyMentionsThinking(body) {
		return body, wireModel, nil
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body, wireModel, suffixIntent
	}
	changed := translator.StripContentTypes(m, providers.ModelStrip(provider, wireModel))
	if h.injectProviderThinkingMode(mode, m) {
		changed = true
	}
	if thinking.NormalizeThinkingConfig(m) {
		changed = true
	}
	intent := suffixIntent
	if intent == nil {
		intent = thinking.CaptureThinking(m)
	}
	if !changed {
		return body, wireModel, intent
	}
	out, err := json.Marshal(m, json.Deterministic(true))
	if err != nil {
		return body, wireModel, intent
	}
	return out, wireModel, intent
}
```

`injectProviderThinkingMode` must therefore return a bool and `thinking.NormalizeThinkingConfig` must return a bool.

- [ ] **Step 11: Make the two helpers report a change**

In `internal/handlers/chat/thinking_wire.go`, change `injectProviderThinkingMode` to `func (h *ChatHandler) injectProviderThinkingMode(mode string, m map[string]any) bool` — return `true` when it wrote a field.

In `internal/thinking/suffix.go`, change `NormalizeThinkingConfig` to `func NormalizeThinkingConfig(body map[string]any) bool` — return `true` when it deleted the `thinking` block.

- [ ] **Step 12: Write the failing test — the inbound pass keeps the bytes**

In `internal/handlers/chat/thinking_wire_test.go`:

```go
func TestPrepareInboundKeepsAnUnchangedBodyByteIdentical(t *testing.T) {
	h := wireHandler()
	body := []byte(`{"model":"m","user_id":9007199254740993,"messages":[{"role":"user","content":"hi"}]}`)
	out, _, _ := h.prepareInboundThinking("openai", "m", body)
	if !bytes.Equal(out, body) {
		t.Fatalf("an unchanged inbound body must be returned byte for byte:\nin:  %s\nout: %s", body, out)
	}
}
```

- [ ] **Step 13: Run test to verify it passes**

Run: `go test ./internal/handlers/chat/ -run TestPrepareInboundKeepsAnUnchangedBodyByteIdentical -v`
Expected: PASS

- [ ] **Step 14: Run the affected suites**

Run: `go test ./internal/thinking/ ./internal/translator/ ./internal/handlers/chat/ ./internal/proxy/executor/`
Expected: PASS

- [ ] **Step 15: Commit**

```bash
git add internal/thinking/format.go internal/thinking/suffix.go internal/translator/content_strip.go internal/handlers/chat/thinking_wire.go internal/thinking/format_test.go internal/handlers/chat/thinking_wire_test.go
git commit -m "fix(thinking): return the input bytes when the pass changes nothing"
```

---

### Task 4: Operator override dialect and suffix resolution order

**Files:**
- Modify: `internal/handlers/chat/thinking_wire.go` (`injectProviderThinkingMode`, `prepareInboundThinking`)
- Modify: `internal/handlers/chat/chat.go:46-120` (`HandleChatCompletions`) and `:227` (`HandleMessages`)
- Modify: `internal/handlers/chat/responses.go:69-154` (`HandleResponses`)
- Modify: `internal/handlers/chat/resolution.go:223-230` (`resolveModel`)
- Test: `internal/handlers/chat/thinking_wire_test.go`, `internal/handlers/chat/thinking_wire_modes_test.go`, `internal/integration/thinking_test.go`

**Interfaces:**
- Consumes: `thinking.Apply` and `thinking.NormalizeThinkingConfig` from Task 3 (both now return a bool).
- Produces:
  - `thinking.WithSuffixIntent(ctx context.Context, intent *Intent) context.Context` and `thinking.SuffixIntentFromContext(ctx context.Context) *Intent` — new, in `internal/thinking/context.go`.
  - `ChatHandler.prepareInboundThinking(provider, model string, body []byte, claudeClient bool) ([]byte, string, *thinking.Intent)` — **signature change** (adds `claudeClient`).

- [ ] **Step 1: Write the failing test — a Claude client never receives an OpenAI-only field**

In `internal/handlers/chat/thinking_wire_test.go`:

```go
func TestInjectProviderThinkingUsesTheClaudeDialectForAClaudeClient(t *testing.T) {
	h := wireHandler()
	m := map[string]any{"model": "claude-x", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	if !h.injectProviderThinkingMode("high", m, true) {
		t.Fatal("the override must be reported as applied")
	}
	if _, has := m["reasoning_effort"]; has {
		t.Fatalf("a Claude body must not carry reasoning_effort: %+v", m)
	}
	thinking, _ := m["thinking"].(map[string]any)
	if thinking == nil || thinking["type"] != "enabled" {
		t.Fatalf("a Claude body must carry the Claude thinking dialect: %+v", m)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/chat/ -run TestInjectProviderThinkingUsesTheClaudeDialectForAClaudeClient -v`
Expected: FAIL — `reasoning_effort` is written.

- [ ] **Step 3: Make the injected field dialect-aware**

In `internal/handlers/chat/thinking_wire.go`, replace `injectProviderThinkingMode`:

```go
func (h *ChatHandler) injectProviderThinkingMode(mode string, m map[string]any, claudeClient bool) bool {
	mode, valid := thinking.NormalizeProviderThinkingMode(mode)
	if !valid || mode == "auto" {
		return false
	}
	applied := false
	switch mode {
	case "on":
		if _, has := m["thinking"]; !has {
			m["thinking"] = map[string]any{"type": "enabled", "budget_tokens": 10000}
			applied = true
		}
	case "off":
		if _, has := m["thinking"]; !has {
			m["thinking"] = map[string]any{"type": "disabled"}
			applied = true
		}
	default:
		if claudeClient {
			// A Claude Messages body reaches Anthropic unchanged: an
			// OpenAI-only reasoning_effort is an unknown field there and is
			// answered with a 400. The Claude dialect carries the level.
			if _, has := m["thinking"]; !has {
				if mode == "none" {
					m["thinking"] = map[string]any{"type": "disabled"}
				} else {
					m["thinking"] = map[string]any{"type": "enabled", "budget_tokens": thinking.BudgetForLevel(mode)}
				}
				applied = true
			}
			break
		}
		if _, has := m["reasoning_effort"]; !has {
			m["reasoning_effort"] = mode
			applied = true
		}
	}
	return applied
}
```

- [ ] **Step 4: Add the level→budget helper**

In `internal/thinking/levels.go`:

```go
// BudgetForLevel returns the thinking budget a Claude-shaped body carries for
// an effort level. An unknown level uses the medium budget.
func BudgetForLevel(level string) int {
	if b, ok := EffortToBudget(level); ok && b > 0 {
		return b
	}
	return LevelToBudget["medium"]
}
```

- [ ] **Step 5: Pass the client dialect into the inbound pass**

In `internal/handlers/chat/thinking_wire.go`, change the signature to `prepareInboundThinking(provider, model string, body []byte, claudeClient bool)` and pass `claudeClient` to `injectProviderThinkingMode`.

In `internal/handlers/chat/fallback.go`, at the `prepareInboundThinking` call site, pass the endpoint-derived flag:

```go
body, wireModel, thinkingIntent := h.prepareInboundThinking(provider, model, body, f.Endpoint == "/v1/v1/messages" || f.Endpoint == "/v1/messages")
```

- [ ] **Step 6: Run the chat suite**

Run: `go test ./internal/handlers/chat/`
Expected: PASS

- [ ] **Step 7: Write the failing test — a suffixed alias resolves**

In `internal/handlers/chat/thinking_wire_test.go`:

```go
func TestParseSuffixStripsBeforeModelResolution(t *testing.T) {
	base, intent := thinking.ParseSuffix("mycombo(high)")
	if base != "mycombo" {
		t.Fatalf("base = %q, want mycombo", base)
	}
	if intent == nil || intent.Mode != thinking.ModeLevel || intent.Level != "high" {
		t.Fatalf("intent = %+v", intent)
	}
}
```

and add an integration case in `internal/integration/thinking_test.go` that registers a combo named `mycombo` and posts `mycombo(high)`, asserting the upstream received the combo's first model and a `reasoning_effort` of `high`.

- [ ] **Step 8: Run test to verify it fails**

Run: `go test -tags=integration ./internal/integration/ -run TestSuffixOnAComboName -v`
Expected: FAIL — the combo name does not resolve.

- [ ] **Step 9: Carry the suffix intent on the context**

Create `internal/thinking/context.go`:

```go
package thinking

import "context"

type suffixIntentKey struct{}

// WithSuffixIntent carries the reasoning intent a "(level)" model suffix
// expressed, from the point the model name is read to the point the request
// is dispatched.
func WithSuffixIntent(ctx context.Context, intent *Intent) context.Context {
	if intent == nil {
		return ctx
	}
	return context.WithValue(ctx, suffixIntentKey{}, intent)
}

// SuffixIntentFromContext returns the intent a "(level)" model suffix
// expressed, or nil when the model name carried none.
func SuffixIntentFromContext(ctx context.Context) *Intent {
	if ctx == nil {
		return nil
	}
	intent, _ := ctx.Value(suffixIntentKey{}).(*Intent)
	return intent
}
```

- [ ] **Step 10: Strip the suffix at the HTTP entry points**

In `internal/handlers/chat/chat.go` `HandleChatCompletions`, replace the `resolveModel` call site:

```go
modelName, suffixIntent := thinking.ParseSuffix(reqBody.Model)
ctx = thinking.WithSuffixIntent(ctx, suffixIntent)
modelInfo, err := h.resolveModel(modelName)
```

and use `modelName` in the `translator.WithRequestedModel(ctx, ...)` call in the same handler. Apply the same two lines in `HandleMessages` and in `internal/handlers/chat/responses.go` `HandleResponses`, using the model each handler reads from its body.

- [ ] **Step 11: Prefer the carried intent in the inbound pass**

In `internal/handlers/chat/thinking_wire.go` `prepareInboundThinking`, replace the suffix derivation with:

```go
wireModel, suffixIntent := thinking.ParseSuffix(model)
if carried := thinking.SuffixIntentFromContext(ctx); carried != nil {
	suffixIntent = carried
}
```

and give `prepareInboundThinking` the `ctx context.Context` parameter.

- [ ] **Step 12: Run the chat and integration suites**

Run: `go test ./internal/handlers/chat/ && go test -tags=integration ./internal/integration/`
Expected: PASS

- [ ] **Step 13: Commit**

```bash
git add internal/thinking/context.go internal/thinking/levels.go internal/handlers/chat/thinking_wire.go internal/handlers/chat/fallback.go internal/handlers/chat/chat.go internal/handlers/chat/responses.go internal/handlers/chat/thinking_wire_test.go internal/integration/thinking_test.go
git commit -m "fix(chat): write the operator override in the client's dialect and strip the level suffix before resolution"
```

---

### Task 5: Coverage and reproducible benchmarks

**Files:**
- Create: `internal/db/settings_bench_test.go`
- Create: `internal/translator/openai_filter_bench_test.go`
- Modify: `internal/integration/thinking_test.go`
- Modify: `.changes/531-thinking-normalization-review-fixes.md`

**Interfaces:**
- Consumes: `db.NewRepo(db *sql.DB) *Repo` and `translator.FilterToOpenAIFormatJSON(body []byte, opts FilterOptions) []byte` — both unchanged.
- Produces: `BenchmarkGetSettings`, `BenchmarkFilterToOpenAIFormatJSON` — benchmark functions, no API change.

- [ ] **Step 1: Add the settings benchmark**

Create `internal/db/settings_bench_test.go`:

```go
package db

import "testing"

func BenchmarkGetSettings(b *testing.B) {
	database, cleanup := setupTestDB(b)
	defer cleanup()
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY, data TEXT NOT NULL);`); err != nil {
		b.Fatalf("create settings table: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?)`, `{"providerThinking":{"deepseek":{"mode":"off"}}}`); err != nil {
		b.Fatalf("seed settings: %v", err)
	}
	repo := NewRepo(database)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := repo.GetSettings(); err != nil {
			b.Fatalf("GetSettings: %v", err)
		}
	}
}
```

`setupTestDB` takes a `testing.TB`; if it does not, add a `setupTestDBTB` variant in the same file.

- [ ] **Step 2: Add the filter benchmark**

Create `internal/translator/openai_filter_bench_test.go`:

```go
package translator

import "testing"

func BenchmarkFilterToOpenAIFormatJSON(b *testing.B) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{}}}}]}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = FilterToOpenAIFormatJSON(body, FilterOptions{})
	}
}
```

- [ ] **Step 3: Run the benchmarks**

Run: `go test ./internal/db/ ./internal/translator/ -run XXX -bench 'GetSettings|FilterToOpenAIFormatJSON' -benchmem`
Expected: both benchmarks report `N/op` and `B/op` — the numbers the changelog quotes are now reproducible.

- [ ] **Step 4: Add the Responses-dialect disable coverage**

In `internal/integration/thinking_test.go`, add a case that posts `reasoning: {"effort":"none"}` to the muse provider and asserts the upstream received `reasoning.effort == "minimal"` and no `reasoning_effort` field:

```go
func TestMuseResponsesLaneClampsTheResponsesDialectDisable(t *testing.T) {
	// … newProviderEnv-style fixture with a muse connection …
	body := ChatBody("muse/muse-spark-1.3-contributor", false)
	body["reasoning"] = map[string]any{"effort": "none"}
	res := env.Post(t, "/v1/chat/completions", body)
	// assert 200, then parse up.Last(t).Body and check reasoning.effort == "minimal"
	// and that "reasoning_effort" is absent
}
```

- [ ] **Step 5: Run the integration suite**

Run: `go test -tags=integration ./internal/integration/ -run 'Muse|Thinking' -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/db/settings_bench_test.go internal/translator/openai_filter_bench_test.go internal/integration/thinking_test.go
git commit -m "test: benchmark the settings read and the OpenAI filter, and cover the Responses-dialect disable"
```

---

### Task 6: Minor findings, documentation and the changelog fragment

**Files:**
- Modify: `internal/providers/cline_identity.go:18` (Windows platform spelling)
- Modify: `internal/providers/cline_identity_test.go`
- Modify: `internal/thinking/format.go:182-184` (the dead `commandcode` target branch) and `internal/thinking/format_writers_test.go`
- Modify: `internal/proxy/executor/commandcode_request.go:230-233` (dangling comment)
- Modify: `internal/thinking/writers_table.go:243-245` (stale comment)
- Modify: `internal/handlers/chat/thinking_wire.go` (precedence comment)
- Modify: `internal/handlers/chat/combo.go` and `internal/handlers/chat/mimofree.go` (mimo-free claim)
- Modify: `internal/db/settings.go` (deep-copy the cached maps) and `internal/db/settings_test.go`
- Modify: `internal/providers/capabilities.go` (stray blank lines)
- Modify: `internal/translator/gemini_test.go:547` (test name)
- Modify: `internal/integration/muse_reasoning_clamp_test.go`, `internal/integration/claude_thinking_disabled_test.go` (field-level assertions)
- Modify: `internal/handlers/chat/thinking_wire_test.go:58` (field-level assertion)
- Modify: `internal/thinking/scan_test.go:57-66` (drop the wall-clock bound)
- Modify: `.changes/531-thinking-normalization-review-fixes.md`

**Interfaces:**
- Consumes: `thinking.NormalizeProviderThinkingMode` and `thinking.EffortToBudget` (unchanged).
- Produces: `db.CloneSettings` stays unexported; `db.cloneSettings` gains a deep copy of the seven maps.

- [ ] **Step 1: Fix the Windows platform spelling**

In `internal/providers/cline_identity.go`, map the Go platform to the Node spelling upstream sends:

```go
plat := runtime.GOOS
if plat == "windows" {
	plat = "win32" // Node's process.platform spelling (upstream buildClineHeaders)
}
```

and use `plat` for `X-PLATFORM`.

- [ ] **Step 2: Fix the dangling comment in `commandcodeMaxTokens`**

In `internal/proxy/executor/commandcode_request.go`, replace the reference to the deleted note with the reason inline:

```go
// commandcodeMaxTokens mirrors upstream `body.max_tokens ?? body.max_output_tokens
// ?? DEFAULT_MAX_TOKENS`. The fallback stays 1024 (upstream 64000): the
// provider rejects a budget larger than the free tier it is billed against.
```

- [ ] **Step 3: Fix the stale `writeCommandcode` comment**

In `internal/thinking/writers_table.go`, replace the comment above `writeCommandcode` with one that describes the whitelist:

```go
// writeCommandcode emits the CommandCode dialect: the native CLI sends
// reasoning_effort inside the params of the /alpha/generate envelope. Go's
// flat OpenAI body has no params object, so the field is written at the top
// level and buildCommandcodeBody copies it into params — it is one of the
// fields the whitelist carries across.
```

- [ ] **Step 4: Correct the precedence comment**

In `internal/handlers/chat/thinking_wire.go`, replace the comment above `injectProviderThinkingMode` with:

```go
// injectProviderThinkingMode applies settings.providerThinking[provider].mode,
// mirroring upstream chatCore.js:117-131. The guard is per dialect: the
// operator fills a gap the client left in that dialect, and where two
// dialects collide the precedence is Extract's, not the override's — a
// client-set reasoning_effort outranks an injected thinking block.
```

- [ ] **Step 5: Deep-copy the cached settings maps**

In `internal/db/settings.go`, make `cloneSettings` copy each map it shares:

```go
func cloneSettings(s *SettingsData) *SettingsData {
	if s == nil {
		return nil
	}
	out := *s
	out.RTKCategories = cloneBoolMap(s.RTKCategories)
	out.RTKFilters = cloneBoolMap(s.RTKFilters)
	out.ComboStrategies = cloneComboStrategies(s.ComboStrategies)
	out.ProviderStrategies = cloneProviderStrategies(s.ProviderStrategies)
	out.CapacityAdapter = cloneCapacityAdapters(s.CapacityAdapter)
	out.ProviderOverrides = cloneProviderOverrides(s.ProviderOverrides)
	out.ProviderThinking = cloneProviderThinking(s.ProviderThinking)
	return &out
}
```

with one small clone helper per map type in the same file.

- [ ] **Step 6: Write the failing test — a caller cannot poison the cache**

In `internal/db/settings_test.go`:

```go
func TestGetSettingsCloneDoesNotShareMaps(t *testing.T) {
	// seed a settings row with a providerThinking entry
	// read it, mutate settings.ProviderThinking["deepseek"].Mode = "off"
	// read it again and assert the mode is still the seeded value
}
```

- [ ] **Step 7: Run test to verify it passes**

Run: `go test ./internal/db/ -run TestGetSettingsCloneDoesNotShareMaps -v`
Expected: PASS

- [ ] **Step 8: Fix the remaining minor findings**

- `internal/handlers/chat/thinking_wire_test.go:58` — replace the `strings.Contains(out, "reasoning_effort")` key-presence check with an unmarshal and a map lookup.
- `internal/integration/muse_reasoning_clamp_test.go:40` and `internal/integration/claude_thinking_disabled_test.go:39` — unmarshal `sent.Body` and assert the parsed field equals `"minimal"`, and that the disable value is absent.
- `internal/translator/gemini_test.go:547` — rename the test to `TestTranslateOpenAIToGeminiThinkingOutputFloor`.
- `internal/thinking/scan_test.go:57-66` — delete the wall-clock bound assertion; keep the allocation and result assertions.
- `internal/providers/capabilities.go` — remove the two stray blank lines the diff introduced.
- `internal/thinking/format.go` — the `target == "commandcode"` early return is dead in production (dispatch only produces `openai`, `openai-responses` and `claude`); the commandcode writer is reached through the capability overlay at `target "openai"`. Add the missing route to `internal/thinking/format_writers_test.go`:

```go
func TestApplyCommandcodeThroughTheCapabilityOverlay(t *testing.T) {
	out := apply(t, "openai", "commandcode", "deepseek/deepseek-v4-pro",
		map[string]any{"model": "deepseek/deepseek-v4-pro", "reasoning_effort": "max"}, nil)
	if out["reasoning_effort"] != "max" {
		t.Fatalf("overlay route → %+v", out)
	}
}
```

- [ ] **Step 9: Run the affected suites**

Run: `go test ./internal/db/ ./internal/providers/ ./internal/thinking/ ./internal/translator/ ./internal/handlers/chat/ && go test -tags=integration ./internal/integration/`
Expected: PASS

- [ ] **Step 10: Extend the changelog fragment**

In `.changes/531-thinking-normalization-review-fixes.md`, append a `### Fixed` block covering: the deterministic Claude translation, the Responses dialect winning over the provider registry, the byte-identity of an unchanged body, the operator override written in the client's dialect, the `(level)` suffix resolved before the model lookup, the tokenrouter disable clamp, the tools normalisation without a `messages` key, and the `GOOS` platform spelling.

- [ ] **Step 11: Verify §7.6 compliance**

Run:

```bash
{ git diff --name-only HEAD; git diff --name-only main...HEAD; git status --porcelain | cut -c4-; } | grep -x CHANGELOG.md && echo "VIOLATION" || echo CLEAN
```

Expected: `CLEAN`

- [ ] **Step 12: Commit**

```bash
git add internal/providers/cline_identity.go internal/providers/cline_identity_test.go internal/providers/capabilities.go internal/thinking/format.go internal/thinking/format_writers_test.go internal/thinking/writers_table.go internal/thinking/scan_test.go internal/proxy/executor/commandcode_request.go internal/handlers/chat/thinking_wire.go internal/handlers/chat/thinking_wire_test.go internal/handlers/chat/combo.go internal/handlers/chat/mimofree.go internal/db/settings.go internal/db/settings_test.go internal/translator/gemini_test.go internal/integration/muse_reasoning_clamp_test.go internal/integration/claude_thinking_disabled_test.go .changes/531-thinking-normalization-review-fixes.md
git commit -m "chore: clear the minor findings from the second thinking review"
```

---

## Verification

After every task:

```bash
go build ./...
go vet ./internal/thinking/... ./internal/translator/... ./internal/providers/... ./internal/handlers/chat/... ./internal/db/...
go test ./...
go test -count=1 -tags=integration ./internal/integration/...
gofmt -l $(git diff --name-only upstream/main...HEAD | grep '\.go$')
```

Expected: build, vet, both suites and gofmt clean.

Then, once:

```bash
git fetch upstream && git update-ref refs/heads/main upstream/main
```

so a PR opened against `main` does not carry the 39 upstream commits local `main` is missing (AGENTS.md §7.7).
