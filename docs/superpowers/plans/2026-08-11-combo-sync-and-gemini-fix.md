# Combo Sync and Gemini Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Synchronize the Go combo strategy with Next.js intelligence (per-combo state, proper auto-switch order, comprehensive capability detection) and fix the Gemini `thought_signature` API error during Claude-to-Gemini translation.

**Architecture:** 
- `gemini.go`: Inject `thought` field into Gemini `functionCall` items to satisfy API requirements for thinking models.
- `combo.go`: Swap the execution order of strategy vs capability detection. Use a per-combo rotation state map. Use `capabilities.go` for detection instead of hardcoded provider maps.

**Tech Stack:** Go, testing/quick

## Global Constraints

- Do not remove Go's Connection Locking and Nested Combo features.
- Tests must pass. Do not leave the build broken.
- Use `go test` for running tests.

---

### Task 1: Gemini `thought_signature` Proxy Fix

**Files:**
- Modify: `internal/proxy/gemini.go`
- Test: `internal/proxy/gemini_test.go`

**Interfaces:**
- Produces: Correctly mapped Gemini `functionCall` objects containing a `thought` property when converting from Anthropic's `tool_use`.

- [ ] **Step 1: Write the failing test**

```go
// In internal/proxy/gemini_test.go, add a test for Anthropic-to-Gemini message conversion 
// specifically checking for thought_signature in tool_use -> functionCall
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/proxy -run TestGeminiThoughtSignature -v` (or your specific test name)
Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

```go
// In internal/proxy/gemini.go, in the function that converts Anthropic tool_use to Gemini functionCall (e.g. `convertAnthropicMessageToGemini` or similar),
// extract `thought_signature` from the Anthropic block (or provide a default empty string) and map it to `thought` or `thought_signature` inside the Gemini functionCall args or struct as appropriate for the Gemini API schema.
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/proxy -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/proxy
git commit -m "fix(proxy): add thought_signature mapping for gemini function calls"
```

---

### Task 2: Capability Detection Scope and Audio/Video (Combo Sync Part 1)

**Files:**
- Modify: `internal/handlers/chat/combo.go`
- Test: `internal/handlers/chat/combo_test.go`

**Interfaces:**
- Produces: Updated `DetectRequiredCapabilities` that only scans trailing user turn and detects `vision`, `pdf`, `audioInput`, `videoInput`. Updated `ReorderByCapabilities` that uses `GetCapabilitiesForModel` from `providers` package.

- [ ] **Step 1: Write the failing test**

```go
// Update combo_test.go to test trailing turn scanning (ignoring earlier turns) and sorting using full capabilities.
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/chat -run TestDetectRequiredCapabilities -v`
Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

```go
// In combo.go:
// 1. DetectRequiredCapabilities: loop only over trailing user turn in messages array. 
// 2. Add audio/video detection (e.g. base64 audio/video data URIs or types).
// 3. ReorderByCapabilities: use internal/providers/capabilities.go's GetCapabilitiesForModel instead of visionProviders map.
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/handlers/chat -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/chat
git commit -m "feat(combo): trailing turn cap detection and audio/video support"
```

---

### Task 3: Per-Combo State and Auto-Switch Order (Combo Sync Part 2)

**Files:**
- Modify: `internal/handlers/chat/chat.go`
- Modify: `internal/handlers/chat/combo.go`
- Modify: `internal/handlers/chat/types.go`
- Test: `internal/handlers/chat/combo_test.go`

**Interfaces:**
- Produces: `comboRotationState` map per combo. Proper `autoSwitch` ordering.

- [ ] **Step 1: Write the failing test**

```go
// Update combo_test.go to test that rotation happens BEFORE capability overrides, and that separate combos have independent rotation states.
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/chat -run TestComboStrategy -v`
Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

```go
// In types.go/chat.go: Add `ComboRotationState` map and Mutex to `ChatHandler`.
// In combo.go:
// 1. Update `applyComboStrategy` to use the per-combo name as a key instead of global rrIdx.
// 2. In `handleComboFallback` and `handleMessagesComboFallback`, apply `applyComboStrategy` FIRST, then `ReorderByCapabilities`.
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/handlers/chat -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/chat
git commit -m "feat(combo): per-combo rotation state and correct auto-switch order"
```
