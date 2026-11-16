# Claude Passthrough Integrity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the `/v1/messages` lane from silently dropping or corrupting whatever a new Claude Code CLI sends, and stop the stalled-tool-loop guard from killing turns.

**Architecture:** Three allowlists become denylists — block emptiness, message keys, and (already landed in PR #1216) the beta header. A regression corpus built from 2.1.325 payload shapes pins the forward-unknown behaviour. The guard keeps detecting but only logs.

**Tech Stack:** Go 1.27, `encoding/json/v2` (deterministic marshal), existing `internal/translator`, `internal/handlers/chat`, `internal/resilience`, `internal/log`.

**Spec:** `docs/superpowers/specs/2026-11-16-claude-passthrough-integrity-design.md`

## Global Constraints

- A body with nothing to fix is returned **byte for byte**; every sanitizer reports `changed` and callers rely on it.
- Unknown block type or message key ⇒ forward it (D1). Only names listed as known-bad are removed.
- Registry beta flags are shared map state; request-scoped edits copy, never mutate (already true).
- `reasoning_content`, `output_config`, `tool_calls`, `function_call`, `reasoning` are the known-foreign message keys.
- No new dependency. No change to non-Claude lanes.
- Beta forwarding and `tool_addition`/`tool_removal` content landed in PR #1216; do not re-do them.

## Review Focus

- A block type the gateway has never seen (`future_block`) inside an assistant turn: must reach the API unchanged, not be read as empty.
- A message key outside the denylist (`cache_config`) on a user turn: must survive to the API, where a 400 names it — silent stripping is the failure being fixed.
- A `tool_use` block with `input: null`: must stay content, must not be rewritten to `{}` (D4).
- A client that sends a beta flag the API rejects: request fails with `Unexpected value(s)`, and the log says which flag — never silently dropped.
- A model that reads the same file three times in one turn: turn reaches the model, one log line, agent survives (D7).

---

### Task 1: Block emptiness becomes a denylist

**Files:**
- Modify: `internal/translator/claude_blocks.go` (`contentfulBlocks`, `isContentfulBlock`)
- Test: `internal/translator/claude_blocks_test.go` (create)

**Interfaces:**
- Consumes: nothing.
- Produces: `knownEmptyBlock(block map[string]any) bool` — true only for blank text, unsigned thinking, and an empty `tool_result`; `isContentfulBlock(block map[string]any) bool` keeps its name and signature, now returning `!knownEmptyBlock(block)`.

- [ ] **Step 1: Write the failing test**

```go
func TestKnownEmptyBlock_UnknownTypeIsContent(t *testing.T) {
	cases := []struct {
		name  string
		block map[string]any
		want  bool // contentful
	}{
		{"unknown type survives", map[string]any{"type": "future_block", "payload": 1}, true},
		{"blank text is empty", map[string]any{"type": "text", "text": "  "}, false},
		{"text with words is content", map[string]any{"type": "text", "text": "hi"}, true},
		{"unsigned thinking is empty", map[string]any{"type": "thinking", "thinking": "x"}, false},
		{"tool_use with null input is content", map[string]any{"type": "tool_use", "name": "Bash", "input": nil}, true},
		{"empty tool_result is empty", map[string]any{"type": "tool_result", "content": []any{}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isContentfulBlock(tc.block); got != tc.want {
				t.Errorf("isContentfulBlock = %v, want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/translator/ -run TestKnownEmptyBlock_UnknownTypeIsContent -v`
Expected: FAIL on `unknown type survives` and `tool_use with null input is content` (today both are false).

- [ ] **Step 3: Implement the denylist**

Replace `contentfulBlocks` with `knownEmptyBlock`. Keep the unsigned-thinking rule (a `thinking` block whose `signature` is not `IsValidClaudeSignature`) and the blank-text rule inline, then:

```go
// knownEmptyBlock reports a block that carries nothing Anthropic will accept.
func knownEmptyBlock(block map[string]any) bool
// isContentfulBlock reports whether one content block keeps its message alive.
func isContentfulBlock(block map[string]any) bool { return !knownEmptyBlock(block) }
```

`tool_result` with an empty `content` array or empty string is empty; `tool_use` is never empty regardless of `input`.

- [ ] **Step 4: Run the package**

Run: `go test ./internal/translator/ -count=1`
Expected: PASS, including the existing passthrough suites.

- [ ] **Step 5: Commit**

```bash
git add internal/translator/claude_blocks.go internal/translator/claude_blocks_test.go
git commit -m "fix(translator): treat an unknown block type as content"
```

---

### Task 2: Message keys become a denylist

**Files:**
- Modify: `internal/translator/request.go` (`claudeMessageKeys`, `dropForeignMessageKeys`)
- Test: `internal/translator/claude_message_keys_test.go` (extend)

**Interfaces:**
- Consumes: nothing.
- Produces: `foreignMessageKeys = map[string]struct{}{"reasoning_content":{}, "output_config":{}, "tool_calls":{}, "function_call":{}, "reasoning":{}}`; `dropForeignMessageKeys(msgMap map[string]any) bool` keeps its signature.

- [ ] **Step 1: Write the failing test**

```go
func TestDropForeignMessageKeys_KeepsUnknownKey(t *testing.T) {
	msg := map[string]any{"role": "user", "content": "hi", "cache_config": map[string]any{"ttl": 1}}
	if !dropForeignMessageKeys(msg) {
		t.Error("expected the known-foreign key sweep to report a change")
	}
	if _, ok := msg["cache_config"]; !ok {
		t.Error("an unknown message key must survive to the API")
	}
}
```

Add a companion asserting `output_config` is still removed from a message with content.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/translator/ -run TestDropForeignMessageKeys_KeepsUnknownKey -v`
Expected: FAIL — `cache_config` is deleted today.

- [ ] **Step 3: Implement the denylist**

Rename `claudeMessageKeys` to `foreignMessageKeys` with the five entries from Global Constraints; `dropForeignMessageKeys` deletes only those.

- [ ] **Step 4: Run the package**

Run: `go test ./internal/translator/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/translator/request.go internal/translator/claude_message_keys_test.go
git commit -m "fix(translator): drop only the message keys known to be foreign"
```

---

### Task 3: Log what the sanitizer removes

**Files:**
- Modify: `internal/translator/request.go` (`SanitizeClaudePassthrough`)
- Test: `internal/translator/claude_sanitize_log_test.go` (create)

**Interfaces:**
- Consumes: `foreignMessageKeys` (Task 2), `knownEmptyBlock` (Task 1), `log.Debug` from `9router/proxy/internal/log`.
- Produces: `sanitizeDropLog` — `[]string` of `key:<name>` / `block:<type>` collected during one body's sanitize, emitted as a single `log.Debug("translator", "claude sanitize removed", "items", strings.Join(...))`. Nothing is emitted when `changed` is false.

- [ ] **Step 1: Write the failing test**

```go
func TestSanitizeClaudePassthrough_ReportsDrops(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":16,"messages":[
		{"role":"user","content":[{"type":"text","text":"   "}],"reasoning_content":"x"},
		{"role":"assistant","content":[{"type":"text","text":"ok"}]}
	]}`)
	req := decodeBody(t, SanitizeClaudePassthrough(body, false))
	if _, ok := req["messages"].([]any); !ok {
		t.Fatal("expected messages to survive")
	}
	got := lastSanitizeDropLog()
	for _, want := range []string{"key:reasoning_content", "block:text"} {
		if !strings.Contains(got, want) {
			t.Errorf("drop log missing %q, got %q", want, got)
		}
	}
}
```

`lastSanitizeDropLog` is a test-only seam: package-level `var sanitizeDropSink func(string)` set by the test, called instead of `log.Debug` when non-nil.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/translator/ -run TestSanitizeClaudePassthrough_ReportsDrops -v`
Expected: FAIL — `undefined: lastSanitizeDropLog`.

- [ ] **Step 3: Implement the collector**

Collect names at each removal site already in `SanitizeClaudePassthrough` (foreign key, empty block, empty turn, lifted system turn, dropped tool call). Emit once at the end through `sanitizeDropSink` if set, else `log.Debug`.

- [ ] **Step 4: Run the package**

Run: `go test ./internal/translator/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/translator/request.go internal/translator/claude_sanitize_log_test.go
git commit -m "feat(translator): log what the claude sanitizer removes"
```

---

### Task 4: The stalled-tool-loop guard logs and never blocks

**Files:**
- Modify: `internal/handlers/chat/stalled_tool_loop_intervention.go` (`CheckStalledToolLoopOpenAI`, `CheckStalledToolLoopClaude`)
- Modify: `internal/handlers/chat/chat.go:85-96`, `internal/handlers/chat/chat.go:378-389`
- Test: `internal/handlers/chat/stalled_tool_loop_intervention_test.go`

**Interfaces:**
- Consumes: `resilience.InspectHistoryTurns(messages []map[string]any, threshold int) *StalledLoopIntervention`.
- Produces: both `Check*` functions keep their signatures and now always return `false`, writing nothing; they log `log.Warn("resilience", "stalled tool loop detected", "tool", i.ToolName, "count", i.CallCount, "model", model)` and leave the turn to the model.

- [ ] **Step 1: Write the failing test**

Change the existing "OpenAI loop is handled" and "Claude loop is handled" cases to:

```go
handled := h.CheckStalledToolLoopOpenAI(rec, messages, "gpt-4o", false)
if handled {
	t.Fatal("the guard must not handle the turn any more")
}
if rec.Body.Len() != 0 {
	t.Errorf("the guard must write nothing, got %q", rec.Body.String())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/chat/ -run TestStalledToolLoop -v`
Expected: FAIL — both still return true and write a body.

- [ ] **Step 3: Implement log-only**

Replace the body of each `Check*` with the detect-and-log above, returning `false`. The call sites stay (`if h.Check… { return }` never fires), so `chat.go` needs no edit — leave it, and say so in the commit body.

- [ ] **Step 4: Run the package**

Run: `go test ./internal/handlers/chat/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/chat/stalled_tool_loop_intervention.go internal/handlers/chat/stalled_tool_loop_intervention_test.go
git commit -m "fix(resilience): log a stalled tool loop instead of replacing the turn"
```

---

### Task 5: Regression corpus from 2.1.325 payload shapes

**Files:**
- Create: `internal/translator/claude_cli_corpus_test.go`

**Interfaces:**
- Consumes: `SanitizeClaudePassthrough(body []byte, intentionalPrefill bool) []byte`; `TranslateClaudeToOpenAI([]byte) ([]byte, error)`.
- Produces: nothing; the tests are the deliverable.

- [ ] **Step 1: Write the corpus test**

One table, one entry per Review Focus line:

```go
func TestClaudeCLICorpus(t *testing.T) {
	cases := []struct{ name string; body []byte; wantSubstrings []string; wantAbsent []string }{
		{
			name: "unknown block survives",
			body: []byte(`{"model":"m","max_tokens":8,"messages":[
				{"role":"user","content":[
					{"type":"text","text":"hi"},
					{"type":"future_block","payload":{}}]}]}`),
			wantSubstrings: []string{"future_block"},
		},
		{
			name: "unknown message key survives",
			body: []byte(`{"model":"m","max_tokens":8,"messages":[
				{"role":"user","content":"hi","cache_config":{"ttl":1}}]}`),
			wantSubstrings: []string{"cache_config"},
		},
		{
			name: "tool_addition and tool_removal survive",
			body: []byte(`{"model":"m","max_tokens":8,"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":[{"type":"tool_addition","tool":{"type":"tool_definition","name":"z"}}]},
				{"role":"assistant","content":[{"type":"tool_removal","name":"z"}]},
				{"role":"user","content":"go"}]}`),
			wantSubstrings: []string{"tool_addition", "tool_removal"},
		},
		{
			name: "mid-history system turn is lifted",
			body: []byte(`{"model":"m","max_tokens":8,"messages":[
				{"role":"user","content":"one"},
				{"role":"assistant","content":"two"},
				{"role":"system","content":"reminder"},
				{"role":"user","content":"three"}]}`),
			wantSubstrings: []string{`"system":"reminder"`},
			wantAbsent:     []string{`"role":"system"`},
		},
		{
			name: "foreign key is stripped",
			body: []byte(`{"model":"m","max_tokens":8,"messages":[
				{"role":"user","content":"hi","reasoning_content":"x"}]}`),
			wantAbsent: []string{"reasoning_content"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := string(SanitizeClaudePassthrough(tc.body, false))
			for _, want := range tc.wantSubstrings {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q in %s", want, out)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(out, absent) {
					t.Errorf("unexpected %q in %s", absent, out)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/translator/ -run TestClaudeCLICorpus -v`
Expected: PASS once Tasks 1–3 are in. A failure here is a spec violation, not a test to adjust.

- [ ] **Step 3: Commit**

```bash
git add internal/translator/claude_cli_corpus_test.go
git commit -m "test(translator): pin the claude CLI passthrough corpus"
```

---

### Task 6: Whole-flow verification

**Files:** none.

- [ ] **Step 1: Build and run the affected suites**

```bash
go build ./...
go test ./internal/translator/ ./internal/providers/ ./internal/handlers/chat/ ./internal/resilience/ -count=1
```
Expected: all PASS.

- [ ] **Step 2: Live smoke on the local gateway (after deploy)**

```bash
# unknown block must reach the API, which answers about the block, not about content
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:20130/v1/messages \
  -H "x-api-key: $KEY" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
  -d '{"model":"cc/claude-opus-5-5","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"future_block","payload":{}}]}]}'
```
Expected: 400 naming `future_block` — proof the block was forwarded rather than dropped.

- [ ] **Step 3: Confirm the guard no longer kills a turn**

Run a short agent turn that reads one file three times.
Expected: the turn completes; one `stalled tool loop detected` line in the log.

- [ ] **Step 4: Commit** — nothing to commit; record the two outputs in the plan's PR body.

---

## Self-review

- Spec D1/D2 → Tasks 1, 2. D3 → landed in #1216 (not re-planned, listed in Global Constraints). D4 → holds by construction; pinned by the corpus case "tool_use with null input is content". D5 → landed in #1217. D6 → Tasks 1, 2 share one file, no lane drift. D7 → Task 4.
- Corpus covers the five Review Focus lines: unknown block, unknown key, null-input tool_use, rejected beta (log line from Task 3/`Unexpected value(s)`), repeated reads (Task 4).
- No task defines a symbol another task invents: `knownEmptyBlock`, `foreignMessageKeys`, `sanitizeDropSink`, `Check*` keep names from their own task's Interfaces block.
