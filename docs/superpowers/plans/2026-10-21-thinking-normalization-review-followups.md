# Thinking Normalisation Review Follow-ups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close every finding from the code review of `fix/thinking-normalization` against the canonical `main` (`upstream/main`) — the dropped `(level)` suffix on Responses-native lanes, the per-request cost of the new thinking pass, and the minor defects — without breaking upstream parity.

**Architecture:** The unified thinking pass stays where it is: `internal/thinking` owns the intent model and the per-format writers, and `internal/handlers/chat/fallback.go` applies it at the dispatch choke point. The Responses-native lane stops being a special case: it gets the same `ApplyJSON` call as every other lane, with the wire dialect resolved to `openai-responses` instead of being skipped. The per-request cost is reduced by making the two always-on passes (the thinking-key scan and the OpenAI filter) skip work they provably do not change.

**Tech Stack:** Go 1.27, `encoding/json/v2` (`json.Deterministic(true)`), `net/http/httptest`, `modernc.org/sqlite` via `internal/db`, Svelte 5 (not touched by this plan).

**Spec:** This plan implements the review of the branch `fix/thinking-normalization`; the behaviour it must match is upstream `decolua/9router@master`:
- `open-sse/translator/index.js:93,137,143` — `captureThinking` before translation, `applyThinking(targetFormat, …)` after it, `filterToOpenAIFormat` for an OpenAI target.
- `open-sse/translator/concerns/thinkingUnified.js:34-49,55-110,127-146,427-446` — `parseSuffix`, `extractThinking` precedence, `resolveFormat`, `applyThinking`.
- `open-sse/handlers/chatCore.js:115-127` — the `providerThinking` injection chain.

## Global Constraints

- **Upstream is the behavioural spec.** A change that diverges from upstream needs the divergence recorded in `.changes/<pr>-<slug>.md`, never in `CHANGELOG.md` (§7.6).
- **Provider isolation (§3):** no provider-specific branch may leak into a generic path. The `provider == "muse"` special case in `resolveFormat` is generalised away in Task 1, not extended.
- **No hardcoded model rewrites (§3.B):** routing decisions come from the registry and the capability table, never from a substring test on a model id.
- **Small-hardware budget (§4.G):** every change on a per-request path must not raise steady RSS or per-request CPU. A change that skips work must skip work it provably does not change.
- **Go style (§4.D):** files ≤ 300 LoC, cognitive complexity ≤ 15 per function, guard clauses, errors wrapped with `%w`, no `panic()`.
- **Tests (§5):** `testing.T` only, table-driven, `httptest` fakes, no network, no sleeps or poll loops.
- **Feature integration tests (§5.D):** live under `internal/integration/` behind the `integration` build tag, use the `newEnv`/`newProviderEnv`/`env.AddConnection` helpers, and assert observable behaviour (the wire body the upstream received).
- **Every new test module stays under the project file-size threshold** (~300 LoC). Split a test file rather than growing it past the limit.

## Review Focus

Five input classes the upstream spec implies and no task's test exercises yet:

1. **A `(level)` suffix on a Responses-native provider** (`muse`, `opencode`, `opencode-zen`, any provider whose base URL ends in `/responses`) — the user expects the named effort to reach the upstream, not the upstream default. Owned by Task 1.
2. **A client that sets `reasoning_effort` while the operator configured `providerThinking`** — the operator's `off` must not silently re-enable reasoning, and the client's field must not be silently rewritten. Owned by Task 2.
3. **A request body that mentions the word `reasoning` inside a string value** (a user message saying "explain the reasoning") — the fast path must not treat it as a thinking dialect, and the body must not be rewritten. Owned by Task 4 and Task 5.
4. **A 400 KB request body** — the per-request scan and decode cost must stay bounded and must not allocate a copy of the body. Owned by Task 5 and Task 6.
5. **A provider that cannot disable reasoning, reached with a disable request in a dialect the clamp does not read** (`thinking:{type:"disabled"}` on a Responses lane) — the request must reach the upstream clamped to the lowest level, never as a disable. Owned by Task 1.

---

### Task 1: Apply the thinking pass on the Responses-native lane

**Files:**
- Modify: `internal/thinking/format.go` — `resolveFormat` (the `provider == "muse"` special case)
- Modify: `internal/handlers/chat/fallback.go` — the outbound thinking pass (currently `internal/handlers/chat/fallback.go:585-606`)
- Test: `internal/thinking/format_writers_test.go`
- Test: `internal/integration/muse_reasoning_clamp_test.go`

**Interfaces:**
- Consumes: `thinking.ApplyJSON(body []byte, target, provider, model string, override *Intent) []byte` (unchanged), `thinking.Intent` (unchanged), `executor.UpstreamSpeaksResponses(provider, model string, cfg *providers.ProviderConfig) bool` (unchanged).
- Produces: `resolveFormat` returns `"openai-responses"` whenever the target format is `"openai-responses"` and the capability does not declare a native-only dialect. No new exported symbol.

- [ ] **Step 1: Write the failing unit test**

In `internal/thinking/format_writers_test.go`, add:

```go
// TestApplyJSONOpenAIResponsesWritesNestedReasoning pins the Responses wire
// dialect: a strict Responses upstream rejects a top-level reasoning_effort
// and reads reasoning:{effort,summary} instead.
func TestApplyJSONOpenAIResponsesWritesNestedReasoning(t *testing.T) {
	body := []byte(`{"model":"muse-spark-1.3-contributor","reasoning_effort":"high"}`)
	out := thinking.ApplyJSON(body, "openai-responses", "muse", "muse-spark-1.3-contributor", nil)
	var wire map[string]any
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := wire["reasoning_effort"]; has {
		t.Errorf("top-level reasoning_effort reached a Responses wire: %s", out)
	}
	r, ok := wire["reasoning"].(map[string]any)
	if !ok || r["effort"] != "high" {
		t.Errorf("reasoning = %v, want {effort:high}", wire["reasoning"])
	}
}
```

(The test file is in package `thinking`, so call `ApplyJSON` without the package qualifier.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/thinking/ -run TestApplyJSONOpenAIResponsesWritesNestedReasoning -v`
Expected: FAIL — `resolveFormat` returns the capability's `"openai"` dialect for a non-muse provider, so the body keeps `reasoning_effort` at the top level.

- [ ] **Step 3: Generalise `resolveFormat`**

In `internal/thinking/format.go`, replace the muse-only special case inside the `caps.ThinkingFormat` branch with a target-driven rule placed before it:

```go
	isOpenAIWire := target == "openai" || target == "openai-responses"
	// A Responses target is served by the Responses dialect: a strict
	// Responses upstream rejects a top-level reasoning_effort and reads
	// reasoning:{effort,summary} (upstream thinkingUnified.js:137-145).
	if target == "openai-responses" && !nativeOnlyFormats[caps.ThinkingFormat] {
		return "openai-responses"
	}
```

and delete the `provider == "muse"` branch that follows it. The native-only guard keeps a gemini/claude/kiro capability from being rewritten as a Responses dialect.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/thinking/ -run TestApplyJSONOpenAIResponses -v`
Expected: PASS.

- [ ] **Step 5: Apply the pass on the Responses lane at the dispatch choke point**

In `internal/handlers/chat/fallback.go`, replace the outbound thinking block with:

```go
	if !claudeNative {
		target := "openai"
		switch {
		case executor.UpstreamSpeaksResponses(provider, wireModel, providerCfg):
			target = "openai-responses"
		case isAnthropic:
			target = "claude"
		}
		pipedBody = thinking.ApplyJSON(pipedBody, target, provider, wireModel, thinkingIntent)
		if target == "openai" && !providerCfg.IsGeminiNative() {
			pipedBody = translator.FilterToOpenAIFormatJSON(pipedBody, translator.FilterOptions{
				PreserveCacheControl: providers.PreservesCacheControl(provider),
			})
		}
	}
```

The separate `thinking.ClampDisableToMinimalJSON` call goes away: `ApplyJSON` reads the same dialects (`reasoning_effort`, `reasoning.effort`, `thinking.type`) and the `none`-on-a-model-that-cannot-disable clamp is part of the writer, so the Responses lane now gets the clamp *and* the suffix *and* the dialect conversion.

- [ ] **Step 6: Write the failing integration test**

In `internal/integration/muse_reasoning_clamp_test.go`, add:

```go
// TestMuseResponsesLaneAppliesTheModelSuffix pins the suffix on a
// Responses-native lane: the named effort must reach the upstream instead of
// the upstream default. Before the fix the suffix was parsed and dropped.
func TestMuseResponsesLaneAppliesTheModelSuffix(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n"))
	})
	priority := 1
	data := `{"apiKey":"sk-muse","baseUrl":"` + up.URL + `/chat/completions"}`
	if err := env.Repo.CreateProviderConnectionFull("conn-muse", "muse", "apikey", "Muse", &priority, data); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	res := env.Post(t, "/v1/chat/completions", ChatBody("muse/muse-spark-1.3-contributor(high)", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	sent := up.Last(t)
	if got := sent.Model(t); got != "muse-spark-1.3-contributor" {
		t.Errorf("upstream model = %q, want muse-spark-1.3-contributor", got)
	}
	if !strings.Contains(string(sent.Body), `"effort":"high"`) {
		t.Errorf("the suffix effort did not reach the upstream: %s", truncate(sent.Body))
	}
}
```

- [ ] **Step 7: Run the integration test to verify it fails, then passes**

Run: `go test -tags=integration ./internal/integration/ -run TestMuseResponsesLaneAppliesTheModelSuffix -v`
Expected before Step 5's change: FAIL — the upstream body carries no reasoning field. After: PASS.

- [ ] **Step 8: Run the whole affected suite**

Run: `go test ./internal/thinking/... ./internal/handlers/chat/... ./internal/translator/... && go test -tags=integration ./internal/integration/...`
Expected: all packages ok.

- [ ] **Step 9: Commit**

```bash
git add internal/thinking/format.go internal/thinking/format_writers_test.go internal/handlers/chat/fallback.go internal/integration/muse_reasoning_clamp_test.go
git commit -m "fix(thinking): apply the level suffix on Responses-native lanes"
```

---

### Task 2: Pin the `providerThinking` precedence and record the upstream divergence

**Files:**
- Test: `internal/handlers/chat/thinking_wire_test.go`
- Modify: `.changes/1005-thinking-normalization-parity.md`

**Interfaces:**
- Consumes: `thinking.NormalizeProviderThinkingMode(mode string) (string, bool)`, `thinking.ProviderThinkingModes` (both unchanged), `(*ChatHandler).injectProviderThinking(provider string, m map[string]any)` (unchanged).
- Produces: no new exported symbol. The behaviour stays as it is — upstream's chain (`chatCore.js:117-127`) only fills a gap the client left, and the client's `reasoning_effort` outranks the injected `thinking` block in `extractThinking` on both sides.

- [ ] **Step 1: Write the tests that pin the precedence**

In `internal/handlers/chat/thinking_wire_test.go`, add a fixture and two tests. The fixture follows the package's existing pattern (`setupChatTestDB(t)` → `db.NewRepo(database)` → `NewChatHandler(repo)`, with the settings row seeded with `database.Exec`, as `vision_adapter_e2e_test.go` does):

```go
// thinkingHandler returns a ChatHandler whose settings row carries a
// providerThinking override for the provider.
func thinkingHandler(t *testing.T, provider, mode string) *ChatHandler {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)
	if _, err := database.Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		`{"providerThinking":{"`+provider+`":{"mode":"`+mode+`"}}}`,
	); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	return NewChatHandler(db.NewRepo(database))
}

// TestProviderThinkingOffDoesNotOverrideAClientReasoningField pins upstream's
// precedence (chatCore.js:117-127): the operator override fills a gap the
// client left, and a client-set reasoning_effort outranks the injected
// thinking block in extractThinking on both sides.
func TestProviderThinkingOffDoesNotOverrideAClientReasoningField(t *testing.T) {
	h := thinkingHandler(t, "deepseek", "off")
	m := map[string]any{"reasoning_effort": "high"}
	h.injectProviderThinking("deepseek", m)
	intent := thinking.Extract(m)
	if intent == nil || intent.Mode != thinking.ModeLevel || intent.Level != "high" {
		t.Fatalf("intent = %+v, want the client's level high", intent)
	}
}

// TestProviderThinkingOffAppliesWhenTheClientIsSilent pins the other half:
// with no client reasoning field the operator's disable is what the pipeline
// sees.
func TestProviderThinkingOffAppliesWhenTheClientIsSilent(t *testing.T) {
	h := thinkingHandler(t, "deepseek", "off")
	m := map[string]any{}
	h.injectProviderThinking("deepseek", m)
	intent := thinking.Extract(m)
	if intent == nil || intent.Mode != thinking.ModeNone {
		t.Fatalf("intent = %+v, want mode none", intent)
	}
}
```

- [ ] **Step 2: Run the tests**

Run: `go test ./internal/handlers/chat/ -run 'TestProviderThinkingOff' -v`
Expected: PASS — the current implementation already matches upstream. These tests exist to make the precedence deliberate and visible, not to change it.

- [ ] **Step 3: Record the divergence in the changelog fragment**

In `.changes/1005-thinking-normalization-parity.md`, add to the `### Fixed` section:

```markdown
- **The `providerThinking` override never puts an invalid effort on the wire.** Upstream's injection chain (`chatCore.js:117-127`) falls through to `body.reasoning_effort = mode` when the client already set `body.thinking`, which writes the literal `on`/`off` as an effort — a value no provider accepts. This port keeps the chain's precedence (the operator fills a gap the client left; a client-set `reasoning_effort` outranks the injected `thinking` block) but refuses the fall-through, so `on`/`off` only ever reach the wire as the `thinking` dialect they were meant to be.
```

- [ ] **Step 4: Commit**

```bash
git add internal/handlers/chat/thinking_wire_test.go .changes/1005-thinking-normalization-parity.md
git commit -m "test(chat): pin the providerThinking precedence and record the divergence"
```

---

### Task 3: Stop reading the settings row twice per request

**Files:**
- Modify: `internal/db/settings.go` — `(*Repo).GetSettings`
- Test: `internal/db/settings_test.go`

**Interfaces:**
- Consumes: `(*Repo).GetSettings() (*SettingsData, error)` (unchanged signature), `SettingsData.ProviderThinking map[string]ProviderThinking`.
- Produces: `(*Repo).GetSettings` returns the cached value when the settings row has not changed. The cache is invalidated by the write path in the same file.

- [ ] **Step 1: Write the failing test**

In `internal/db/settings_test.go`, add:

```go
// TestGetSettingsReflectsAWrite pins the cache contract: a write must be
// visible to the next read, so the cache is validated against the row
// content and never serves a stale settings blob.
func TestGetSettingsReflectsAWrite(t *testing.T) {
	repo := newTestRepo(t)
	before, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	before.ProviderThinking = map[string]ProviderThinking{"deepseek": {Mode: "off"}}
	if err := repo.SaveSettings(before); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	after, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings after write: %v", err)
	}
	if after.ProviderThinking["deepseek"].Mode != "off" {
		t.Errorf("providerThinking = %+v, want the written value", after.ProviderThinking)
	}
}
```

Use the repository constructor the other tests in `internal/db/settings_test.go` use; if that file opens the database differently, follow it.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/db/ -run TestGetSettingsReflectsAWrite -v`
Expected: FAIL until the write path invalidates the cache — the test is the contract, and it must pass at the end of this task.

- [ ] **Step 3: Add the cache**

The read costs **94.5 µs/op, 5.6 KB, 53 allocs** on the reference machine: a point query (47 µs) plus a JSON parse and the map building (the rest). The thinking pass calls it on every request, and the fast path used to call it twice.

Cache the *parse*, not the row: the row is always read, so the answer is never stale, and the parsed value is reused when the row content is unchanged. A TTL cache was rejected — two existing tests write the settings row with direct SQL (not through the Repo) and read it back immediately, and a TTL cache served them a stale value.

In `internal/db/repos.go`, extend the struct:

```go
	// settingsCache holds the parsed settings row and the row content it was
	// parsed from. It lives on the Repo rather than in package state because
	// the answer belongs to one database: two Repos over different handles
	// would otherwise read each other's settings.
	settingsCache atomic.Pointer[cachedSettings]
```

with

```go
// cachedSettings is one parsed settings row and the row content it was
// parsed from.
type cachedSettings struct {
	raw   string
	value *SettingsData
}
```

In `internal/db/settings.go`:

- split the current `GetSettings` into `readSettingsRow` (the `SELECT`) and `parseSettings` (the parse plus the map building);
- `GetSettings` reads the row, and when the cached entry's `raw` equals it, returns a copy of the cached value; otherwise it parses, stores the entry and returns a copy;
- `cloneSettings` copies the struct so a caller that replaces a field cannot poison the cached value. The maps inside are shared and are read-only to every caller outside this package.

Do not cache `GetSettingsRaw`: the write path merges into it and must always read the current row.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/db/ -run TestGetSettings -v`
Expected: PASS.

- [ ] **Step 5: Split the file**

`internal/db/settings.go` was 712 lines before this change (the repo's rule: modularize anything over 400). Split it into three files, all under 300:

- `internal/db/settings.go` — the types, `DefaultSettings`, `GetSettings`, `cloneSettings`, `readSettingsRow`, `settingsInt`, `stringMap`;
- `internal/db/settings_parse.go` — `parseSettings`;
- `internal/db/settings_writes.go` — the setters (`SetAutoUpdate`, `SetProviderStrategy`, `SetComboStrategy`, `GetProviderOverride`, `SetProviderOverride`, `GetGuardrailsEnabled`, `SetGuardrailsEnabled`, `GetRateLimitDefaults`, `SetRateLimitDefaults`).

A pure move: no logic changes, the imports follow the functions.

- [ ] **Step 6: Run the whole database suite**

Run: `go test ./internal/db/...`
Expected: ok.

- [ ] **Step 7: Commit**

```bash
git add internal/db/settings.go internal/db/settings_test.go
git commit -m "perf(db): cache the settings row the thinking pass reads per request"
```

---

### Task 4: Give the OpenAI filter a fast path

**Files:**
- Modify: `internal/translator/openai_filter.go` — `FilterToOpenAIFormatJSON`
- Modify: `internal/translator/openai_filter_messages.go` — the helpers that report whether they changed anything
- Test: `internal/translator/openai_filter_determinism_test.go`

**Interfaces:**
- Consumes: `FilterToOpenAIFormatJSON(body []byte, opts FilterOptions) []byte` (unchanged signature), `FilterOptions{PreserveCacheControl bool}` (unchanged).
- Produces: no new exported symbol. `FilterToOpenAIFormatJSON` returns the **input bytes unchanged** when the filter would not change the body; otherwise it returns the deterministically marshalled body.

- [ ] **Step 1: Write the failing test**

In `internal/translator/openai_filter_determinism_test.go`, add:

```go
// TestFilterToOpenAIFormatJSONLeavesAnUnchangedBodyByteIdentical pins the
// fast path: a body the filter would not change is returned byte for byte,
// so a plain request does not pay a re-serialisation.
func TestFilterToOpenAIFormatJSONLeavesAnUnchangedBodyByteIdentical(t *testing.T) {
	body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	out := FilterToOpenAIFormatJSON(body, FilterOptions{})
	if string(out) != string(body) {
		t.Errorf("body was rewritten:\n got %s\nwant %s", out, body)
	}
}

// TestFilterToOpenAIFormatJSONFastPathMatchesTheSlowPath pins the fast path
// against the filter itself: for every body in the corpus the byte-identical
// result and the filtered result must agree.
func TestFilterToOpenAIFormatJSONFastPathMatchesTheSlowPath(t *testing.T) {
	corpus := []string{
		`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`,
		`{"model":"gpt-5","messages":[{"role":"user","content":""}]}`,
		`{"model":"gpt-5","messages":[{"role":"developer","content":"x"}]}`,
		`{"model":"gpt-5","messages":[{"role":"user","content":[{"type":"text","text":"a"}]}]}`,
		`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"tools":[]}`,
		`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"any"}}`,
		`{"model":"gpt-5","messages":[{"role":"user","content":[{"type":"thinking","thinking":"x"},{"type":"text","text":"a"}]}]}`,
		`{"model":"gpt-5","messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]}]}`,
	}
	for _, raw := range corpus {
		body := []byte(raw)
		fast := FilterToOpenAIFormatJSON(body, FilterOptions{})
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		FilterToOpenAIFormat(m, FilterOptions{})
		want, err := json.Marshal(m, json.Deterministic(true))
		if err != nil {
			t.Fatalf("marshal %s: %v", raw, err)
		}
		if string(fast) != string(want) {
			t.Errorf("fast path disagrees with the filter for %s:\n got %s\nwant %s", raw, fast, want)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/translator/ -run 'TestFilterToOpenAIFormatJSON' -v`
Expected: FAIL — the current implementation always re-marshals, so the first test sees reordered keys.

- [ ] **Step 3: Track whether the filter changed anything**

In `internal/translator/openai_filter_messages.go`, change the helpers to report a change:

- `stripBlock(block map[string]any, opts FilterOptions) (map[string]any, bool)` — the bool is false when nothing was deleted;
- `filterContentBlocks(parts []any, opts FilterOptions) ([]any, bool)` — the bool is false when no block was dropped and no block was modified;
- `filterMessages(msgs []any, opts FilterOptions) ([]any, bool)` — the bool is false when no message was rewritten and no message was pruned;
- `pruneEmptyMessages(msgs []any) ([]any, bool)` — the bool is false when no message was dropped;
- `normalizeTools(body map[string]any) bool` — the bool is false when the tool list is unchanged;
- `normalizeToolChoice(body map[string]any) bool` — the bool is false when the choice is unchanged.

- [ ] **Step 4: Return the original bytes when nothing changed**

In `internal/translator/openai_filter.go`, keep the exported signature and add the reporting variant under it:

```go
// FilterToOpenAIFormat normalises a body to clean OpenAI shape — port of
// filterToOpenAIFormat in open-sse/translator/formats/openai.js:10-133.
func FilterToOpenAIFormat(body map[string]any, opts FilterOptions) map[string]any {
	filterToOpenAIFormat(body, opts)
	return body
}

// filterToOpenAIFormat normalises the body and reports whether it changed
// anything. The JSON entry point needs the flag: a body the filter does not
// touch is returned byte for byte, so a plain request pays no re-serialisation.
func filterToOpenAIFormat(body map[string]any, opts FilterOptions) bool {
	msgs, ok := body["messages"].([]any)
	if !ok {
		return false
	}
	changed := filterMessages(msgs, opts)
	changed = normalizeTools(body) || changed
	changed = normalizeToolChoice(body) || changed
	return changed
}
```

and in `FilterToOpenAIFormatJSON`:

```go
func FilterToOpenAIFormatJSON(body []byte, opts FilterOptions) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body
	}
	if !filterToOpenAIFormat(m, opts) {
		return body
	}
	out, err := json.Marshal(m, json.Deterministic(true))
	if err != nil {
		return body
	}
	return out
}
```

The decoded map is discarded when nothing changed, so no copy of it is needed.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/translator/ -run 'TestFilterToOpenAIFormatJSON|TestFilterToOpenAIFormat' -v`
Expected: PASS.

- [ ] **Step 6: Measure the effect**

Run: `go test ./internal/translator/ -run XXX -bench BenchmarkFilterToOpenAIFormatJSON -benchmem`
Expected: the unchanged-body case allocates no copy of the body. Record the numbers in the changelog fragment (Task 6).

- [ ] **Step 7: Commit**

```bash
git add internal/translator/openai_filter.go internal/translator/openai_filter_messages.go internal/translator/openai_filter_determinism_test.go
git commit -m "perf(translator): leave an unchanged request body byte-identical"
```

---

### Task 5: Scan the request body without copying it

**Files:**
- Modify: `internal/thinking/suffix.go` — `BodyMentionsThinking`
- Test: `internal/thinking/scan_test.go`

**Interfaces:**
- Consumes: `BodyMentionsThinking(raw []byte) bool` (unchanged signature), `SnapshotKeys` (unchanged).
- Produces: no new exported symbol. The function allocates nothing.

- [ ] **Step 1: Write the failing test**

In `internal/thinking/scan_test.go`, add:

```go
// TestBodyMentionsThinkingDoesNotCopyTheBody pins the allocation contract:
// the scan runs on every request, so it must not copy the body it scans.
func TestBodyMentionsThinkingDoesNotCopyTheBody(t *testing.T) {
	body := []byte(strings.Repeat(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}],`+"\n", 2000))
	allocs := testing.AllocsPerRun(20, func() {
		if BodyMentionsThinking(body) {
			t.Fatal("unexpected hit")
		}
	})
	if allocs > 0 {
		t.Errorf("BodyMentionsThinking allocated %.1f objects per call, want 0", allocs)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/thinking/ -run TestBodyMentionsThinkingDoesNotCopyTheBody -v`
Expected: FAIL — `string(raw)` copies the body.

- [ ] **Step 3: Scan the bytes**

In `internal/thinking/suffix.go`, replace the body of `BodyMentionsThinking`:

```go
func BodyMentionsThinking(raw []byte) bool {
	for _, k := range snapshotKeyBytes {
		if bytes.Contains(raw, k) {
			return true
		}
	}
	return false
}
```

with a package-level `var snapshotKeyBytes = [][]byte{...}` built once from `SnapshotKeys`, and add the `bytes` import. Keep `SnapshotKeys` exported and unchanged — it is the vocabulary other code may want to scan for.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/thinking/ -run 'TestBodyMentionsThinking|TestScan' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/thinking/suffix.go internal/thinking/scan_test.go
git commit -m "perf(thinking): scan the request body without copying it"
```

---

### Task 6: Measure the per-request cost and record the number

**Files:**
- Test: `internal/thinking/scan_test.go`
- Modify: `.changes/1005-thinking-normalization-parity.md`

**Interfaces:**
- Consumes: `BodyMentionsThinking`, `bodyModelHasSuffix` (both unexported/unexported-plus-tests), `translator.FilterToOpenAIFormatJSON`.
- Produces: no new exported symbol. The benchmark names are the artifact.

- [ ] **Step 1: Add the benchmarks**

In `internal/thinking/scan_test.go`, add:

```go
// BenchmarkBodyMentionsThinking measures the per-request scan on a body large
// enough for the cost to show.
func BenchmarkBodyMentionsThinking(b *testing.B) {
	body := []byte(strings.Repeat(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}],`+"\n", 8000))
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if BodyMentionsThinking(body) {
			b.Fatal("unexpected hit")
		}
	}
}
```

- [ ] **Step 2: Run the benchmark and record the number**

Run: `go test ./internal/thinking/ -run XXX -bench BenchmarkBodyMentionsThinking -benchmem`
Expected: `0 allocs/op` and a time on the order of a millisecond for a ~440 KB body on the reference machine.

- [ ] **Step 3: Record the number in the changelog fragment**

In `.changes/1005-thinking-normalization-parity.md`, add a `### Changed` bullet with the measured body size, the time per call and the allocation count, and the same for the filter's unchanged-body path. State the reference machine (CPU model, core count) so the number can be compared later.

- [ ] **Step 4: Commit**

```bash
git add internal/thinking/scan_test.go .changes/1005-thinking-normalization-parity.md
git commit -m "perf(thinking): record the measured per-request cost"
```

---

### Task 7: Clear the minor defects

**Files:**
- Modify: `internal/translator/gemini.go` — the parity comment (line 561)
- Modify: `internal/thinking/suffix.go` — remove `cloneShallow`
- Modify: `internal/providers/thinking_levels.go` — `codexModelLevels` (line 154)
- Modify: `internal/thinking/levels.go` — the `BudgetToEffort` doc comment
- Modify: `.changes/1005-thinking-normalization-parity.md` — the provider counts

**Interfaces:**
- Consumes: `providers.splitThinkingSuffix(model string) (base, suffix string)` (unexported, in `internal/providers/model_upstream_id.go`).
- Produces: no new exported symbol.

- [ ] **Step 1: Fix the stray character**

In `internal/translator/gemini.go`, the comment ends the template literal with a Unicode left double quote. Replace that character with a backtick so the code span closes:

```go
// Parity with `functionCall.id || \`${name}-${Date.now()}-${index}\`` in
```

- [ ] **Step 2: Delete the dead helper**

In `internal/thinking/suffix.go`, delete `cloneShallow` and the `maps` import it needed. `go build ./...` must still pass — nothing references it.

- [ ] **Step 3: Reuse the existing suffix splitter**

In `internal/providers/thinking_levels.go`, replace the inline copy in `codexModelLevels`:

```go
	base, _ := splitThinkingSuffix(model)
```

- [ ] **Step 4: Note the port-surface helper**

In `internal/thinking/levels.go`, extend the `BudgetToEffort` doc comment with one line: it is upstream port surface kept for the effort tables and has no production caller in this repo.

- [ ] **Step 5: Correct the provider counts**

In `.changes/1005-thinking-normalization-parity.md`, the bullet reads "Registry `thinkingFormat` for all 13 upstream providers (`tokenrouter`, eight `openai` aggregators, the `minimax-code` pair as `claude-adaptive`; `tokenharbor` deliberately none)". The registry carries 12: nine `openai` aggregators (openrouter, siliconflow, cloudflare-ai, blackbox, venice, vercel-ai-gateway, codebuddy-cn, codebuddy-intl, iflow), two `claude-adaptive` (minimax-code, minimax-code-global) and one `tokenrouter`. Rewrite the bullet to those numbers.

- [ ] **Step 6: Verify**

Run: `go build ./... && go test ./internal/translator/... ./internal/thinking/... ./internal/providers/...`
Expected: ok.

- [ ] **Step 7: Commit**

```bash
git add internal/translator/gemini.go internal/thinking/suffix.go internal/providers/thinking_levels.go internal/thinking/levels.go .changes/1005-thinking-normalization-parity.md
git commit -m "chore: clear the minor defects from the thinking review"
```

---

### Task 8: Split the writer switch into per-format writers

**Files:**
- Modify: `internal/thinking/writers.go` — `applyFormat`
- Create: `internal/thinking/writers_table.go` — the per-format writer functions
- Test: `internal/thinking/format_writers_test.go`

**Interfaces:**
- Consumes: `Intent`, `providers.Capabilities`, `providers.ThinkingRange` (all unchanged).
- Produces: `applyFormat(fmt string, body map[string]any, cfg *Intent, caps providers.Capabilities, supported []string, display string)` — the signature is unchanged; only its body moves.

- [ ] **Step 1: Run the package suite before the split**

Run: `go test ./internal/thinking/ -v`
Expected: every test passes. The split is a pure move: the per-format writer tests (`TestApplyZai`, `TestApplyClaudeAdaptive`, `TestApplyQwenDeepseekKimiMinimaxHunyuan`, `TestApplyStepOmitsDisable`, `TestApplyTokenRouter`, `TestApplyCommandcode`, `TestApplyOpenAI`, `TestApplyOpenAIKeepsAuto`, `TestApplyDisplay`) are the gate, so no new test is needed.

A "one dialect per body" invariant is **not** a valid guard here: the Z.ai dialect is `thinking:{type:enabled}` *plus* `reasoning_effort`, and the Claude adaptive dialect is `thinking:{type:adaptive}` *plus* `output_config:{effort}`. Two fields are the shape of those dialects, not a leak.

- [ ] **Step 2: Split the switch**

In `internal/thinking/writers.go`, keep `applyFormat` as the dispatcher and move each case body into a named function in the new `internal/thinking/writers_table.go`:

```go
// writerCtx carries what every per-format writer needs.
type writerCtx struct {
	body      map[string]any
	eff       *Intent
	none      bool
	canDisable bool
	supported []string
	display   string
}

func applyFormat(fmt string, body map[string]any, cfg *Intent, caps providers.Capabilities, supported []string, display string) {
	none := cfg.Mode == ModeNone
	cd := caps.CanDisableThinking()
	eff := cfg
	if none && !cd {
		eff = &Intent{Mode: ModeLevel, Level: "minimal"}
	}
	w, ok := formatWriters[fmt]
	if !ok {
		return
	}
	w(&writerCtx{body: body, eff: eff, none: none, canDisable: cd, supported: supported, display: display})
}
```

Each writer keeps its body verbatim from the current switch arm, with `body` → `ctx.body`, `eff` → `ctx.eff`, `none` → `ctx.none`, `cd` → `ctx.canDisable`, `supported` → `ctx.supported` and `display` → `ctx.display`. The `break` statements become `return`.

- [ ] **Step 3: Run the tests**

Run: `go test ./internal/thinking/ -v`
Expected: every test in the package passes, including the format-writer table tests.

- [ ] **Step 4: Check the file sizes**

Run: `wc -l internal/thinking/writers.go internal/thinking/writers_table.go`
Expected: both under 300 lines.

- [ ] **Step 5: Commit**

```bash
git add internal/thinking/writers.go internal/thinking/writers_table.go internal/thinking/format_writers_test.go
git commit -m "refactor(thinking): split the writer switch into per-format writers"
```

---

### Task 9: Verify the whole branch and update the changelog

**Files:**
- Modify: `.changes/1005-thinking-normalization-parity.md`

**Interfaces:**
- Consumes: everything the previous tasks produced.
- Produces: the final fragment text.

- [ ] **Step 1: Build and vet**

Run: `go build ./... && go vet ./internal/...`
Expected: no output, exit 0.

- [ ] **Step 2: Run the unit suites**

Run: `go test ./internal/...`
Expected: every package ok.

- [ ] **Step 3: Run the integration suite**

Run: `go test -tags=integration ./internal/integration/...`
Expected: every package ok.

- [ ] **Step 4: Update the fragment**

In `.changes/1005-thinking-normalization-parity.md`, add the Task 1 fix (the suffix on Responses-native lanes) to the `### Fixed` section, and the Task 4/5/6 numbers to `### Changed`. Keep the fragment's shape: a `###` heading plus bullets, no front matter.

- [ ] **Step 5: Check the changelog rule**

Run: `{ git diff --name-only HEAD; git diff --name-only main...HEAD; git status --porcelain | cut -c4-; } | grep -qx CHANGELOG.md && echo "VIOLATION: CHANGELOG.md must not appear in a PR diff"`
Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add .changes/1005-thinking-normalization-parity.md
git commit -m "docs(changes): record the review follow-ups"
```
