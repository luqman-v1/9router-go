# Combo thought_signature Restore + Turn-Aware + Nested Flatten — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the combo from emitting Gemini `400 thought_signature` and `429 all connections rate-limited` errors by restoring the `thoughtSignature` wire field, making combo rotation turn-aware, and flattening nested combos so rotation actually reaches every leaf model.

**Architecture:** Three independent changes: (A) fix the JSON tag of `GeminiPart.ThoughtSignature` to camelCase in the translator and add a regression test on the emitted bytes; (C1) add `flattenComboModels` in `resolution.go` and use the flattened list as `ComboModels` so `combo-wombo`→`free-tier` rotates over all 4 leaf models; (B) gate rotation-advance on a turn boundary via a private `applyComboStrategy` while keeping the exported `ApplyComboStrategy` signature unchanged.

**Tech Stack:** Go, `go test`, `modernc.org/sqlite` (test DB).

## Global Constraints

- Do not remove connection locking (`comboLockRetryable`) or nested-combo support.
- Public exported API `ApplyComboStrategy` must keep its current 4-arg signature (backward compatible).
- `go test ./...` must pass at every commit. Do not leave the build broken.
- Only the top-level combo strategy governs rotation over the flattened leaf list; inner-combo strategies are organizational and ignored.

---

### Task 1: Restore camelCase `thoughtSignature` on the wire (fix A)

**Files:**
- Modify: `internal/translator/gemini.go:26`
- Test: `internal/translator/gemini_test.go` (`TestThoughtSignatureResponseRoundTrip`)

**Interfaces:**
- Consumes: existing `TranslateOpenAIToGemini(openaiBody []byte) ([]byte, error)` and `TranslateGeminiResponseToOpenAI(body []byte) ([]byte, *OpenAIUsage, error)`.
- Produces: the Gemini-native request body where a signed `functionCall` part is emitted as `"thoughtSignature":"<sig>"` (camelCase), never `"thought_signature"`.

- [ ] **Step 1: Add the failing regression assertions**

In `internal/translator/gemini_test.go`, inside `TestThoughtSignatureResponseRoundTrip`, after the `geminiBytes` value is produced (after the `if err := TranslateOpenAIToGemini...` block) and before the final "Find the assistant/model content" section, add:

```go
	// The native Gemini generateContent endpoint only recognizes the camelCase
	// part field. The 400 "missing thought_signature" regression was caused by
	// emitting snake_case here.
	if !strings.Contains(string(geminiBytes), `"thoughtSignature":"EvEFCu4FAQw..."`) {
		t.Errorf("emitted Gemini part must use camelCase thoughtSignature, got: %s", geminiBytes)
	}
	if strings.Contains(string(geminiBytes), `"thought_signature"`) {
		t.Errorf("emitted Gemini part must NOT use snake_case thought_signature, got: %s", geminiBytes)
	}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/translator -run TestThoughtSignatureResponseRoundTrip -v`
Expected: FAIL on the first new assertion (`got:` body contains `"thought_signature":`).

- [ ] **Step 3: Fix the JSON tag**

In `internal/translator/gemini.go:26`, change:

```go
	ThoughtSignature string                `json:"thought_signature,omitempty"`
```

to:

```go
	ThoughtSignature string                `json:"thoughtSignature,omitempty"`
```

`GeminiPart.UnmarshalJSON` needs no change: with the camel tag, the embedded `*Alias` reads camelCase from responses and the explicit `aux.ThoughtSignatureSnake` still reads snake_case for legacy responses. Do **not** re-add a `Thought` field inside `GeminiFunctionCall`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/translator -v`
Expected: PASS (all translator tests, including the existing snake_case fixture, still pass via the dual-read).

- [ ] **Step 5: Commit**

```bash
git add internal/translator/gemini.go internal/translator/gemini_test.go
git commit -m "fix(translator): emit camelCase thoughtSignature for gemini functionCall parts"
```

---

### Task 2: Flatten nested combos into leaf models (fix C1)

**Files:**
- Modify: `internal/handlers/chat/resolution.go` (add `flattenComboModels`; rewrite combo branch of `resolveModel`)
- Test: `internal/handlers/chat/resolution_test.go`

**Interfaces:**
- Consumes: `h.Repo.GetComboByName(name string)` (existing), `resolveProviderAlias`, `h.resolvePrefixProvider`.
- Produces:
  - `func (h *ChatHandler) flattenComboModels(models []string) ([]string, error)` — recursively expands combo-name entries into concrete `provider/model` leaves in order, dedupes consecutive identical leaves, errors on cycles.
  - `resolveModel` now returns `ComboModels` as the flattened leaf list (was the raw outer list, which hid nested combos from rotation).

- [ ] **Step 1: Write the failing flatten tests**

In `internal/handlers/chat/resolution_test.go`, add:

```go
func TestFlattenComboModels_ExpandsNested(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	inner, _ := json.Marshal([]string{"oc/ling-3.0-flash-free", "gemini/gemini-3.5-flash"})
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		"ft1", "free-tier", "fallback", string(inner), "2026-07-19T00:00:00Z", "2026-07-19T00:00:00Z")

	flat, err := h.flattenComboModels([]string{"free-tier", "openai/gpt-4"})
	if err != nil {
		t.Fatalf("flatten error: %v", err)
	}
	want := []string{"oc/ling-3.0-flash-free", "gemini/gemini-3.5-flash", "openai/gpt-4"}
	if len(flat) != len(want) {
		t.Fatalf("expected %d models, got %d: %v", len(want), len(flat), flat)
	}
	for i := range want {
		if flat[i] != want[i] {
			t.Errorf("expected %v, got %v", want, flat)
			break
		}
	}
}

func TestFlattenComboModels_DedupesConsecutive(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	inner, _ := json.Marshal([]string{"deepseek/deepseek-chat"})
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		"in1", "inner-only", "fallback", string(inner), "2026-07-19T00:00:00Z", "2026-07-19T00:00:00Z")

	// ["inner-only", "deepseek/deepseek-chat"] both expand to the same leaf.
	flat, err := h.flattenComboModels([]string{"inner-only", "deepseek/deepseek-chat"})
	if err != nil {
		t.Fatalf("flatten error: %v", err)
	}
	if len(flat) != 1 || flat[0] != "deepseek/deepseek-chat" {
		t.Errorf("expected [deepseek/deepseek-chat], got %v", flat)
	}
}

func TestFlattenComboModels_Cycle(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	a, _ := json.Marshal([]string{"combo-b"})
	b, _ := json.Marshal([]string{"combo-a"})
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		"ca", "combo-a", "fallback", string(a), "2026-07-19T00:00:00Z", "2026-07-19T00:00:00Z")
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		"cb", "combo-b", "fallback", string(b), "2026-07-19T00:00:00Z", "2026-07-19T00:00:00Z")

	if _, err := h.flattenComboModels([]string{"combo-a"}); err == nil {
		t.Fatal("expected cycle error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/chat -run 'TestFlattenComboModels' -v`
Expected: FAIL — `h.flattenComboModels undefined`.

- [ ] **Step 3: Implement `flattenComboModels`**

In `internal/handlers/chat/resolution.go`, add after `resolveModelEntry`:

```go
// flattenComboModels recursively expands combo-name entries into concrete
// "provider/model" leaves, keeping order and deduping consecutive identical
// leaves so a nested combo can't create pointless rotation slots. Guards
// against cyclic combo references. Inner-combo strategies are not applied
// here; the top-level combo's strategy governs the flattened list.
func (h *ChatHandler) flattenComboModels(models []string) ([]string, error) {
	out := make([]string, 0, len(models))
	seen := make(map[string]bool)
	var walk func([]string) error
	walk = func(ms []string) error {
		for _, m := range ms {
			if !strings.Contains(m, "/") {
				if seen[m] {
					return fmt.Errorf("combo cycle detected at %q", m)
				}
				if combo, err := h.Repo.GetComboByName(m); err == nil && combo != nil && combo.Models != "" {
					var sub []string
					if err := json.Unmarshal([]byte(combo.Models), &sub); err == nil {
						seen[m] = true
						if err := walk(sub); err != nil {
							return err
						}
						delete(seen, m)
						continue
					}
				}
			}
			if len(out) == 0 || out[len(out)-1] != m {
				out = append(out, m)
			}
		}
		return nil
	}
	if err := walk(models); err != nil {
		return nil, err
	}
	return out, nil
}
```

- [ ] **Step 4: Rewrite the combo branch of `resolveModel`**

In `internal/handlers/chat/resolution.go`, replace the whole `// 3. Check if it's a combo name` block (currently lines ~127–157, from `combo, err := h.Repo.GetComboByName(modelStr)` to the closing brace before `// 4. Check common providers`) with:

```go
	// 3. Check if it's a combo name
	combo, err := h.Repo.GetComboByName(modelStr)
	if err == nil && combo != nil && combo.Models != "" {
		var modelStrings []string
		if err := json.Unmarshal([]byte(combo.Models), &modelStrings); err == nil && len(modelStrings) > 0 {
			// Flatten nested combos into concrete leaves so rotation covers
			// every reachable model (a nested combo entry used to collapse to
			// its first leaf, so combo-wombo -> free-tier never rotated).
			flattened, flatErr := h.flattenComboModels(modelStrings)
			if flatErr != nil {
				return nil, flatErr
			}
			if len(flattened) > 0 {
				parts := strings.SplitN(flattened[0], "/", 2)
				provider := resolveProviderAlias(parts[0])
				if _, ok := providers.KnownProviders[provider]; !ok {
					if info := h.resolvePrefixProvider(provider, parts[1]); info != nil {
						info.ComboModels = flattened
						info.Strategy = combo.Strategy
						return info, nil
					}
				}
				return &ModelInfo{
					Provider:    provider,
					Model:       parts[1],
					ComboModels: flattened,
					Strategy:    combo.Strategy,
				}, nil
			}
		}
	}
```

- [ ] **Step 5: Update the existing nested-combo test expectation**

In `internal/handlers/chat/resolution_test.go`, `TestResolveModel_ComboWithNestedComboName` (combo `combo-wombo` = `["inner-only", "deepseek/deepseek-chat"]`), change the assertion at line ~218:

```go
	if len(info.ComboModels) != 2 {
		t.Errorf("expected 2 combo models, got %d", len(info.ComboModels))
	}
```

to:

```go
	if len(info.ComboModels) != 1 || info.ComboModels[0] != "deepseek/deepseek-chat" {
		t.Errorf("expected flattened [deepseek/deepseek-chat], got %v", info.ComboModels)
	}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/handlers/chat -run 'TestFlattenComboModels|TestResolveModel' -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/handlers/chat/resolution.go internal/handlers/chat/resolution_test.go
git commit -m "feat(combo): flatten nested combos into leaf models so rotation covers all models"
```

---

### Task 3: Turn-aware combo rotation (fix B)

**Files:**
- Modify: `internal/handlers/chat/combo.go:206-257` (`ApplyComboStrategy` → wrapper + private `applyComboStrategy`), `:280`, `:455`
- Test: `internal/handlers/chat/combo_test.go`

**Interfaces:**
- Consumes: nothing new (uses `comboStickyState`, `h.stickyState`, `h.stickyMu` as today).
- Produces:
  - `func (h *ChatHandler) ApplyComboStrategy(strategy string, models []string, comboName string, stickyLimit int) []string` — **unchanged signature**; a wrapper that always treats calls as new turns.
  - `func (h *ChatHandler) applyComboStrategy(strategy string, models []string, comboName string, stickyLimit int, newTurn bool) []string` — advances rotation only when `newTurn` is true.
  - `func detectNewTurn(body []byte) bool` — true when the request's most recent user-type message is plain text; false when the last user-type message is a `role:"tool"` result.

- [ ] **Step 1: Write the failing tests**

In `internal/handlers/chat/combo_test.go`, add:

```go
func TestDetectNewTurn(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"user text", `{"messages":[{"role":"user","content":"hello"}]}`, true},
		{"user text array", `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, true},
		{"mid-turn tool result", `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"x","tool_calls":[{"id":"t1"}]},{"role":"tool","tool_call_id":"t1","content":"ok"}]}`, false},
		{"empty body", `{}`, true},
	}
	for _, tt := range tests {
		if got := detectNewTurn([]byte(tt.body)); got != tt.want {
			t.Errorf("%s: detectNewTurn = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestApplyComboStrategy_roundRobinTurnAware(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"a/x", "b/y", "c/z"}

	first := h.applyComboStrategy("round-robin", models, "comboT", 1, true)
	if first[0] != "a/x" {
		t.Errorf("first lead = %s, want a/x", first[0])
	}

	mid1 := h.applyComboStrategy("round-robin", models, "comboT", 1, false)
	if mid1[0] != "a/x" {
		t.Errorf("mid1 lead = %s, want a/x", mid1[0])
	}
	mid2 := h.applyComboStrategy("round-robin", models, "comboT", 1, false)
	if mid2[0] != "a/x" {
		t.Errorf("mid2 lead = %s, want a/x", mid2[0])
	}

	second := h.applyComboStrategy("round-robin", models, "comboT", 1, true)
	if second[0] != "b/y" {
		t.Errorf("second lead = %s, want b/y", second[0])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/handlers/chat -run 'TestDetectNewTurn|TestApplyComboStrategy_roundRobinTurnAware' -v`
Expected: FAIL — `detectNewTurn undefined` and `h.applyComboStrategy undefined`.

- [ ] **Step 3: Add `detectNewTurn`**

In `internal/handlers/chat/combo.go`, add near the top (after imports):

```go
// detectNewTurn reports whether the request body starts a new conversation
// turn. A turn boundary is the most recent plain-text user message; a request
// whose last user-type message is a tool result continues the current turn,
// and the combo must not switch providers mid-turn (Gemini thinking models
// require a thought_signature on every current-turn functionCall, which only
// the model that made the call can provide).
func detectNewTurn(body []byte) bool {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return true
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg := req.Messages[i]
		if msg.Role == "tool" {
			return false
		}
		if msg.Role != "user" {
			continue
		}
		return contentHasText(msg.Content)
	}
	return true
}

// contentHasText reports whether OpenAI message content contains plain text.
func contentHasText(content json.RawMessage) bool {
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s != ""
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err == nil {
		for _, p := range parts {
			if p.Text != "" {
				return true
			}
		}
	}
	return false
}
```

- [ ] **Step 4: Split `ApplyComboStrategy` into wrapper + turn-aware core**

In `internal/handlers/chat/combo.go`, change the existing `ApplyComboStrategy` (currently `func (h *ChatHandler) ApplyComboStrategy(strategy string, models []string, comboName string, stickyLimit int) []string {` ... full body) into two functions. The public one becomes a backward-compatible wrapper:

```go
// ApplyComboStrategy rotates the array of models based on the selected strategy.
// It treats every call as a new turn (backward-compatible wrapper).
func (h *ChatHandler) ApplyComboStrategy(strategy string, models []string, comboName string, stickyLimit int) []string {
	return h.applyComboStrategy(strategy, models, comboName, stickyLimit, true)
}

// applyComboStrategy is ApplyComboStrategy with turn awareness: the rotation
// index advances only on a new turn (newTurn=true), so a mid-turn tool-use
// sequence stays on the same provider/model.
func (h *ChatHandler) applyComboStrategy(strategy string, models []string, comboName string, stickyLimit int, newTurn bool) []string {
	if len(models) <= 1 {
		return models
	}

	switch strategy {
	case "round-robin":
		// Round-robin is just sticky with limit=1
		stickyLimit = 1
		fallthrough
	case "sticky":
		if stickyLimit <= 1 {
			stickyLimit = 1
		}
		h.stickyMu.Lock()
		defer h.stickyMu.Unlock()
		if h.stickyState == nil {
			h.stickyState = make(map[string]*comboStickyState)
		}

		key := comboName
		if key == "" {
			key = "__default__"
		}
		state, exists := h.stickyState[key]
		if !exists {
			state = &comboStickyState{Index: 0, ConsecutiveUseCount: 0}
			h.stickyState[key] = state
		}

		currentIndex := state.Index % len(models)
		rotated := make([]string, len(models))
		for i := 0; i < len(models); i++ {
			rotated[i] = models[(currentIndex+i)%len(models)]
		}

		// Only advance rotation at a turn boundary.
		if newTurn {
			state.ConsecutiveUseCount++
			if state.ConsecutiveUseCount >= stickyLimit {
				state.Index = (currentIndex + 1) % len(models)
				state.ConsecutiveUseCount = 0
			}
		}

		return rotated
	case "capacity":
		fallthrough
	default:
		out := make([]string, len(models))
		copy(out, models)
		return out
	}
}
```

- [ ] **Step 5: Wire turn detection into the combo handlers**

In `internal/handlers/chat/combo.go`:

- At line ~280 (`handleComboFallback`), change:
  ```go
  	models := h.ApplyComboStrategy(strategy, comboModels, comboName, stickyLimit)
  ```
  to:
  ```go
  	models := h.applyComboStrategy(strategy, comboModels, comboName, stickyLimit, detectNewTurn(body))
  ```
- At line ~455 (`handleMessagesComboFallback`), change:
  ```go
  	models := h.ApplyComboStrategy(strategy, comboModels, comboName, stickyLimit)
  ```
  to:
  ```go
  	models := h.applyComboStrategy(strategy, comboModels, comboName, stickyLimit, detectNewTurn(bodyJSON))
  ```
  (`bodyJSON` already exists in that function, created just above for capability detection.)
- Line ~665 (`handleFusion`) keeps the public `h.ApplyComboStrategy(...)` call (each fan-out is a fresh turn).

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/handlers/chat -v`
Expected: PASS — all existing combo tests (which call the public wrapper) plus the two new tests.

- [ ] **Step 7: Commit**

```bash
git add internal/handlers/chat/combo.go internal/handlers/chat/combo_test.go
git commit -m "feat(combo): turn-aware rotation — same model within a tool-use turn, advance only on new turns"
```

---

### Task 4: Remove stray patch script and verify the build

**Files:**
- Delete: `patch_combo.go`

**Interfaces:**
- Consumes: nothing. This is a leftover throwaway `package main` string-patching script used while the combo changes were being applied; it is not part of any package and no longer needed.

- [ ] **Step 1: Delete the file**

```bash
git rm patch_combo.go
```

- [ ] **Step 2: Verify full build and tests**

Run: `go build ./... && go test ./...`
Expected: build OK, all tests PASS.

- [ ] **Step 3: Commit**

```bash
git commit -m "chore: remove stray patch_combo.go throwaway script"
```

---

## Self-Review Notes

- **Spec coverage:** A → Task 1; C1 (incl. dedupe + cycle guard + updated nested test) → Task 2; B (detectNewTurn + turn-aware advance + both handler call sites) → Task 3; housekeeping → Task 4. D (429 retry-once) is explicitly out of scope and not planned.
- **Placeholder scan:** every step carries concrete code; no TBD/TODO/“add validation”.
- **Type consistency:** `applyComboStrategy` (5-arg) and `ApplyComboStrategy` (4-arg wrapper) match across all call sites; `detectNewTurn([]byte) bool` and `contentHasText(json.RawMessage) bool` are used exactly where defined; `flattenComboModels([]string) ([]string, error)` matches Task 2's tests and the `resolveModel` rewrite.
