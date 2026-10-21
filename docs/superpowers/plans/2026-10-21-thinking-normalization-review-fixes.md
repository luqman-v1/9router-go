# Thinking-Normalisation Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close every finding F1–F20 from the 2026-10-21 review of `fix/thinking-normalization` against `main`, so the branch's two headline fixes (mandatory-reasoning clamp, Claude disable intent) actually reach every lane, its deterministic-wire claim holds at every map→bytes boundary, and the PR carries only the feature.

**Architecture:** The work stays inside the branch's existing shape: `internal/thinking` owns reasoning intent and its emission, `internal/translator` owns wire-format conversion, `internal/proxy/executor` owns provider-specific wire quirks. Three fixes move a decision to the right owner — the mandatory-reasoning clamp becomes a reusable `internal/thinking` function that both the writer path and the Responses-native lane call; the Claude disable dialect becomes a `reasoning_effort:"none"` at the Claude→OpenAI conversion so the existing pipeline carries it; determinism becomes a rule enforced at every map→bytes boundary and pinned by direct unit tests. The PR is realigned onto `origin/main` first so the diff is a feature diff.

**Tech Stack:** Go 1.27, module `9router/proxy`, `encoding/json/v2`, stdlib `testing` only (no testify), `httptest` for provider fakes, `go test -tags=integration` for the feature suite.

**Spec:** `docs/superpowers/plans/2026-10-21-thinking-normalization-review-fixes-spec.md`

## Global Constraints

- Go >= 1.27; module path is `9router/proxy`; all imports are `9router/proxy/internal/...`.
- Tests use only the stdlib `testing` package, table-driven, `t.Fatalf` to abort and `t.Errorf` for independent fields. No testify, no Ginkgo.
- No `time.Sleep`, no polling loops, no wall-clock assertions in tests. A duration bound is allowed only where the bound discriminates a 1000x algorithmic difference (Task 6) and must be stated as such in the test comment.
- `go test -run <Name>` exits 0 with "no tests to run" when the name matches nothing. A verification step is only satisfied when the output shows the named test actually RAN (`--- PASS: TestName`); "ok ... [no tests to run]" is a failure, not a pass.
- `json.Marshal` over any map that reaches the wire MUST pass `json.Deterministic(true)`. json/v2's default map ordering is not stable.
- No `panic()` in production code; wrap errors with `%w`; never swallow an error.
- Production files stay <= 300 LoC; a new file over 400 LoC must be split (AGENTS.md §4.D). Test files follow the same target (Task 10).
- Strict provider isolation: no `strings.Contains(model, ...)` provider switching, no cross-provider aliasing, no model hijacking (AGENTS.md §3). Capability data is registry-driven.
- Every behavioural change needs a regression test that fails before the change and passes after it.
- Changelog entries go in a new `.changes/<pr-number>-<slug>.md` fragment. **Never** edit `CHANGELOG.md` (AGENTS.md §7.6).
- `web/dist` is gitignored and `web/embed.go` embeds it, so `web/dist` must exist before any `go build`/`go test` that compiles `cmd/9router-go`, `internal/app`, `internal/handlers` or `internal/web`. If it is missing, copy it from the main worktree — do not rebuild the SPA.
- Prefix every git command with `rtk` (AGENTS.md §8).
- This branch must end up based on `origin/main` and its PR must target `main` (AGENTS.md §7.7).

## Review Focus

These are the inputs and conditions the spec implies but no existing test exercises. Each one is pinned by a test added in the task that owns the code.

1. **A Responses-native client (`/v1/responses`) asking to disable reasoning.** The body is Responses-shaped, so it carries `reasoning.effort`, not `reasoning_effort`. A clamp that only handles the Chat dialect does nothing here. → Task 1, test `TestClampDisableToMinimalJSON` case "nested reasoning.effort none".
2. **A body that is not a JSON object** (a JSON array, a bare string, truncated bytes). The clamp, the strip and the deterministic marshal must return the input unchanged rather than panic or emit an empty object. → Task 1, case "non-object body unchanged"; Task 3, case "malformed body unchanged".
3. **A vendor-prefixed model id** (`muse/muse-spark-1.3-contributor`). The capability lookup must strip the prefix, or the mandatory-reasoning row is missed and the clamp never fires. → Task 1, integration test uses the prefixed id.
4. **A Claude client asking for no reasoning against a provider that forbids disabling.** The intent must survive the Claude→OpenAI conversion and land as `minimal`, not as silence. → Task 2, integration test `TestClaudeDisabledThinkingReachesUpstreamAsMinimal`.
5. **A multi-megabyte body containing thousands of `"model"` substrings.** The scan must stay linear; today it is quadratic and the body is read with no size cap. → Task 6, test `TestBodyModelHasSuffixIsLinearInBodySize`.

---

### Task 0: Realign the branch onto `origin/main` and drop out-of-scope files (F19)

**Files:**
- Modify: `.gitignore` (ignore the agent-tool config paths)
- Delete from the branch: `.omo/omo.jsonc`, `.omp/settings.json`
- Restore from `origin/main`: `AGENTS.md`, `CLAUDE.md`

**Interfaces:**
- Consumes: nothing.
- Produces: a branch whose `git diff --name-only origin/main...HEAD` lists only the feature files, on top of current `origin/main`. Every later task's `git diff --stat` is measured against this base.

- [ ] **Step 1: Record the pre-state**

Run: `rtk git log --oneline origin/main..HEAD | wc -l` and `rtk git diff --name-only origin/main...HEAD`
Expected: 23 commits, and a file list that includes `.omo/omo.jsonc`, `.omp/settings.json`, `AGENTS.md`, `CLAUDE.md` among the 53 files.

- [ ] **Step 2: Drop the agent-tool config from the branch and ignore it**

Run:
```bash
rtk git rm --cached .omo/omo.jsonc .omp/settings.json
printf '\n# local agent-tooling config (not part of the product)\n.omo/omo.jsonc\n.omp/settings.json\n' >> .gitignore
```
Expected: `git status` shows both files as deleted from the index; the working-tree copies stay on disk.

- [ ] **Step 3: Restore the doc files from `origin/main`**

Run: `rtk git checkout origin/main -- AGENTS.md CLAUDE.md`
Expected: `rtk git diff --name-only origin/main...HEAD` no longer lists `AGENTS.md` or `CLAUDE.md`. The tooling guidance the branch added lives in `AGENTS.md` on `main` already; if `rtk git diff origin/main -- AGENTS.md` is non-empty after this step, `main` does not carry it — in that case keep the branch's version and note the divergence in the PR body instead.

- [ ] **Step 4: Rebase onto `origin/main`**

Run: `rtk git rebase origin/main`
Expected: the 23 commits replay. Conflicts are expected in files `main` also changed. Resolve with this rule: a file the branch **added** (`internal/thinking/*`, `internal/translator/openai_filter.go`, `internal/translator/content_strip.go`, `internal/proxy/executor/commandcode_request.go`, `internal/handlers/chat/thinking_wire.go`) takes the branch's version; a file the branch **modified** takes `main`'s version and then has the branch's hunk re-applied by hand; any other file takes `main`'s version. Run `rtk git rebase --continue` after each resolution.

- [ ] **Step 5: Verify the rebase left the branch working**

Run:
```bash
go build ./... && go test ./... -count=1
```
Expected: build exits 0; the suite passes except `TestForwardQoder_UnknownModelNamesTheModel`, which fails on DNS for `api3.qoder.sh` (pre-existing, not in this diff — see the spec's V3).

- [ ] **Step 6: Confirm the diff is now a feature diff**

Run: `rtk git diff --name-only origin/main...HEAD`
Expected: only `internal/**`, `.changes/**` and `.gitignore`. No `AGENTS.md`, `CLAUDE.md`, `.omo/`, `.omp/`.

- [ ] **Step 7: Commit**

```bash
rtk git add .gitignore AGENTS.md CLAUDE.md
rtk git commit -m "chore(branch): realign fix/thinking-normalization onto origin/main

Drop the local agent-tooling config (.omo/omo.jsonc, .omp/settings.json) and
the AGENTS.md/CLAUDE.md rewrites from the feature branch, and rebase onto
origin/main so the PR diff contains only the thinking-normalisation work."
```

---

### Task 1: Apply the mandatory-reasoning clamp on the Responses-native lane (F1)

**Files:**
- Create: `internal/thinking/clamp.go`
- Create: `internal/thinking/clamp_test.go`
- Create: `internal/integration/muse_reasoning_clamp_test.go`
- Modify: `internal/providers/capabilities.go` (export the tri-state resolver)
- Modify: `internal/thinking/format.go:272` (replace the local `canDisable` with the exported one)
- Modify: `internal/handlers/chat/fallback.go:481-490` (call the clamp on the Responses-native lane)

**Interfaces:**
- Consumes: `providers.Capabilities.ThinkingCanDisable *bool` (existing field), `providers.GetCapabilitiesForModel(provider, model) Capabilities` (existing, `internal/providers/capabilities.go:680`).
- Produces:
  - `func ClampDisableToMinimalJSON(body []byte, caps providers.Capabilities) []byte` — `internal/thinking/clamp.go`. Rewrites an explicit reasoning disable to `"minimal"` when the provider forbids disabling; returns the input unchanged otherwise. Marshals with `json.Deterministic(true)`.
  - `func (c Capabilities) CanDisableThinking() bool` — `internal/providers/capabilities.go`. Exported form of the existing unexported `canDisableThinking` (`internal/providers/capabilities.go:119`).
  - `internal/thinking`'s `canDisable(caps)` is deleted; `writers.go` calls `caps.CanDisableThinking()` instead.

- [ ] **Step 1: Write the failing unit test**

Create `internal/thinking/clamp_test.go` with `TestClampDisableToMinimalJSON`, table-driven over `(body string, canDisable bool, want string)`:

| body | canDisable | want |
|---|---|---|
| `{"reasoning_effort":"none"}` | false | `{"reasoning_effort":"minimal"}` |
| `{"reasoning_effort":"off"}` | false | `{"reasoning_effort":"minimal"}` |
| `{"reasoning_effort":"NONE"}` | false | `{"reasoning_effort":"minimal"}` |
| `{"reasoning":{"effort":"none"}}` | false | `{"reasoning":{"effort":"minimal"}}` |
| `{"reasoning_effort":"none"}` | true | `{"reasoning_effort":"none"}` |
| `{"reasoning_effort":"high"}` | false | `{"reasoning_effort":"high"}` |
| `{"model":"m"}` | false | `{"model":"m"}` |
| `[1,2,3]` | false | `[1,2,3]` |
| `not json` | false | `not json` |

The `canDisable=false` rows use `providers.Capabilities{ThinkingCanDisable: new(false)}`; `canDisable=true` uses `providers.Capabilities{}` (nil flag means disable is allowed).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/thinking/ -run TestClampDisableToMinimalJSON -v`
Expected: FAIL — build error, `undefined: ClampDisableToMinimalJSON`.

- [ ] **Step 3: Implement the clamp**

Create `internal/thinking/clamp.go` with:

- `func ClampDisableToMinimalJSON(body []byte, caps providers.Capabilities) []byte` — returns `body` unchanged when `caps.CanDisableThinking()` is true, when the body does not decode to a JSON object, or when nothing changed. Otherwise decodes into `map[string]any`, applies the rewrite, and re-marshals with `json.Deterministic(true)`; a marshal error returns the input unchanged.
- `func clampDisableValue(m map[string]any) bool` — rewrites `m["reasoning_effort"]` and `m["reasoning"].(map[string]any)["effort"]` when the value is `"none"`/`"off"` (case-insensitive, trimmed) to `"minimal"`, and reports whether it changed anything. The Responses dialect is handled because the Responses-native lanes never run `ApplyJSON`.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/thinking/ -run TestClampDisableToMinimalJSON -v`
Expected: PASS, all 9 cases.

- [ ] **Step 5: Export the tri-state resolver and de-duplicate**

In `internal/providers/capabilities.go`, add:

```go
// CanDisableThinking reports whether thinking can be turned off for this
// model. The tri-state is the one the unexported canDisableThinking already
// resolves: an unset ThinkingCanDisable means the provider allows disabling,
// and the flag's value is the answer when it is set.
func (c Capabilities) CanDisableThinking() bool {
	return c.ThinkingCanDisable == nil || *c.ThinkingCanDisable
}
```

Then delete `internal/thinking/format.go:272` (`func canDisable(caps providers.Capabilities) bool`) and replace its call sites in `internal/thinking/writers.go` with `caps.CanDisableThinking()`.

- [ ] **Step 6: Run the affected packages**

Run: `go test ./internal/thinking/ ./internal/providers/ -count=1`
Expected: PASS.

- [ ] **Step 7: Wire the clamp into the dispatch branch**

In `internal/handlers/chat/fallback.go`, the branch currently reads:

```go
if !claudeNative && !executor.UpstreamSpeaksResponses(provider, wireModel, providerCfg) {
```

Change it to:

```go
if !claudeNative {
    if executor.UpstreamSpeaksResponses(provider, wireModel, providerCfg) {
        // The Responses-native lanes never run ApplyJSON, so the
        // mandatory-reasoning clamp has to happen here or an explicit
        // "none" reaches a provider that rejects it.
        pipedBody = thinking.ClampDisableToMinimalJSON(pipedBody, providers.GetCapabilitiesForModel(provider, wireModel))
    } else {
```

and keep the existing `target`/`ApplyJSON`/`FilterToOpenAIFormatJSON` calls inside the `else` branch, unchanged. `internal/handlers/chat` already imports `internal/providers` and `internal/thinking`.

- [ ] **Step 8: Write the failing integration test**

Create `internal/integration/muse_reasoning_clamp_test.go` (build tag `integration`). Use the harness in `internal/integration/upstream_test.go`:

- `env, up := newProviderEnv(t)`
- `env.AddConnection(t, "muse-1", "muse", "muse", up, "test-key")` — signature `AddConnection(t *testing.T, id, provider, name string, up *Upstream, apiKey string)`
- `env.Post(t, "/v1/chat/completions", map[string]any{"model": "muse/muse-spark-1.3-contributor", "messages": []any{map[string]any{"role": "user", "content": "hi"}}, "reasoning_effort": "none"})`
- Assert on `up.Last(t).Body` (type `RecordedRequest`, field `Body []byte`): the body does **not** contain `"none"` and **does** contain `"minimal"`.

The model id is vendor-prefixed on purpose — the capability lookup must strip the prefix (Review Focus #3).

- [ ] **Step 9: Run the integration test to verify it fails**

Run: `go test -tags=integration ./internal/integration/ -run TestMuseResponsesLaneClampsDisabledReasoning -v -count=1`
Expected: FAIL — the upstream body still contains `"none"`.

- [ ] **Step 10: Verify the integration test passes**

Run: `go test -tags=integration ./internal/integration/ -run TestMuseResponsesLaneClampsDisabledReasoning -v -count=1`
Expected: PASS.

- [ ] **Step 11: Commit**

```bash
rtk git add internal/thinking/clamp.go internal/thinking/clamp_test.go internal/thinking/format.go internal/thinking/writers.go internal/providers/capabilities.go internal/handlers/chat/fallback.go internal/integration/muse_reasoning_clamp_test.go
rtk git commit -m "fix(thinking): clamp a disabled-reasoning request on the Responses-native lane

ApplyJSON never runs when UpstreamSpeaksResponses is true — always the case
for the muse provider — so an explicit reasoning_effort:none reached the
provider unchanged and the mandatory-reasoning clamp never fired. Clamp at the
dispatch chokepoint instead, for both the Chat and the Responses dialect."
```

---

### Task 2: Carry a Claude client's `thinking:{type:"disabled"}` into the pipeline (F2)

**Files:**
- Modify: `internal/translator/request.go:436-449` (the `creq.Thinking` branch in `TranslateClaudeToOpenAI`)
- Modify: `internal/translator/request_test.go`
- Create: `internal/integration/claude_thinking_disabled_test.go`

**Interfaces:**
- Consumes: `thinking.Extract` semantics — `reasoning_effort:"none"` is already read as `ModeNone` by `internal/thinking/intent.go:62-64`, and `writers.go` clamps it to `minimal` for a provider that forbids disabling.
- Produces: `TranslateClaudeToOpenAI` sets `oreq.ReasoningEffort = "none"` for `creq.Thinking.Type == "disabled"`. No new exported symbol.

- [ ] **Step 1: Write the failing unit test**

Add `TestTranslateClaudeToOpenAI_ThinkingDisabled` to `internal/translator/request_test.go`: table over `thinking` input → expected `reasoning_effort` in the converted body:

| input `thinking` | expected `reasoning_effort` |
|---|---|
| `{"type":"disabled"}` | `"none"` |
| `{"type":"enabled","budget_tokens":1000}` | `"low"` (existing mapping, unchanged) |
| `{"type":"adaptive"}` | `"high"` (existing mapping, unchanged) |
| absent | `""` |

Decode the result with `json.Unmarshal` into a struct with a `ReasoningEffort string \`json:"reasoning_effort"\`` field and assert on it.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/translator/ -run TestTranslateClaudeToOpenAI_ThinkingDisabled -v`
Expected: FAIL — the `disabled` row gets `""` instead of `"none"`.

- [ ] **Step 3: Implement the mapping**

In `internal/translator/request.go`, inside the `if creq.Thinking != nil` block of `TranslateClaudeToOpenAI`, add a third branch:

```go
} else if creq.Thinking.Type == "disabled" {
    oreq.ReasoningEffort = "none"
}
```

Place it after the `"enabled"` branch and before the `"adaptive"` branch. `none` is the OpenAI dialect the rest of the pipeline already understands: `thinking.Extract` turns it into `ModeNone`, and the writer path clamps it to `minimal` for a provider that forbids disabling.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/translator/ -run TestTranslateClaudeToOpenAI_ThinkingDisabled -v`
Expected: PASS, all 4 rows.

- [ ] **Step 5: Write the failing integration test**

Create `internal/integration/claude_thinking_disabled_test.go` (build tag `integration`): one `muse` connection to a fake upstream, `env.Post(t, "/v1/messages", map[string]any{"model": "muse/muse-spark-1.3-contributor", "max_tokens": 64, "messages": []any{map[string]any{"role": "user", "content": "hi"}}, "thinking": map[string]any{"type": "disabled"}})`, then assert `up.Last(t).Body` contains `"minimal"` and not `"none"`.

**If the `/v1/messages` route is not mounted by the integration router**, stop and report that as a blocked task — do not silently drop the test and do not substitute a different endpoint. The unit test from Step 1 stays either way.

- [ ] **Step 6: Run the integration test to verify it fails, then passes**

Run: `go test -tags=integration ./internal/integration/ -run TestClaudeDisabledThinkingReachesUpstreamAsMinimal -v -count=1`
Expected: FAIL first (the upstream sees no reasoning field at all), PASS after the fix is in place through the route.

- [ ] **Step 7: Commit**

```bash
rtk git add internal/translator/request.go internal/translator/request_test.go internal/integration/claude_thinking_disabled_test.go
rtk git commit -m "fix(translator): carry a Claude disabled-thinking request into the pipeline

TranslateClaudeToOpenAI handled thinking.type enabled and adaptive but not
disabled, so the disable request was dropped before the thinking chokepoint
could capture it and a Claude client silently got the provider default."
```

---

### Task 3: Make every map→bytes wire boundary deterministic (F3, F4, F5)

**Files:**
- Modify: `internal/handlers/chat/thinking_wire.go:37`
- Modify: `internal/translator/gemini.go:458`, `:465`, `:581`, `:669`, `:737`, `:871`
- Modify: `internal/translator/request.go:631`, `:658`, `:851`, `:864`
- Create: `internal/translator/openai_filter_determinism_test.go`
- Modify: `internal/handlers/chat/thinking_wire_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: no new exported symbol. Every listed call site becomes `json.Marshal(v, json.Deterministic(true))`.

- [ ] **Step 1: Write the failing tests**

Create `internal/translator/openai_filter_determinism_test.go` with two table-free tests. Call the functions with the signatures they have **at this point in the plan** — Task 7 adds an options parameter to `FilterToOpenAIFormatJSON`, and that task updates these two tests when it changes the signature.

- `TestFilterToOpenAIFormatJSONDeterministic` — take one body containing at least six top-level keys, marshal it through `FilterToOpenAIFormatJSON` 100 times, assert every result is byte-identical to the first.
- `TestTranslateOpenAIToGeminiDeterministic` — take one OpenAI body with a `reasoning_effort`, call `translator.TranslateOpenAIToGemini` 100 times, assert every result is byte-identical to the first.

Add `TestPrepareInboundThinkingDeterministic` to `internal/handlers/chat/thinking_wire_test.go`: call `h.prepareInboundThinking("openai", "gpt-4o", body)` 100 times with a body that has several top-level keys, assert every returned body is byte-identical to the first.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/translator/ ./internal/handlers/chat/ -run Deterministic -v -count=1`
Expected: FAIL — the repeated marshals differ in key order.

- [ ] **Step 3: Add `json.Deterministic(true)` to the inbound marshal**

In `internal/handlers/chat/thinking_wire.go`, change:

```go
out, err := json.Marshal(m)
```

to:

```go
out, err := json.Marshal(m, json.Deterministic(true))
```

- [ ] **Step 4: Add `json.Deterministic(true)` to the remaining boundaries**

In `internal/translator/gemini.go`, add the option to the `json.Marshal` calls at lines 458, 465, 581, 669, 737 and 871. In `internal/translator/request.go`, add it to the calls at lines 631, 658, 851 and 864. Leave the `json.Unmarshal` calls untouched.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/translator/ ./internal/handlers/chat/ -run Deterministic -v -count=1`
Expected: PASS.

- [ ] **Step 6: Run the determinism sweep**

Run: `go test ./internal/translator/ ./internal/handlers/chat/ ./internal/thinking/ -run Deterministic -count=100`
Expected: PASS, no flakes. (The `TestApplyJSON` byte-compare flake this repo hit before is why the sweep is part of the gate.)

- [ ] **Step 7: Commit**

```bash
rtk git add internal/handlers/chat/thinking_wire.go internal/handlers/chat/thinking_wire_test.go internal/translator/gemini.go internal/translator/request.go internal/translator/openai_filter_determinism_test.go
rtk git commit -m "fix(translator): marshal every wire boundary deterministically

json/v2 does not order Go map keys by default, so the same request produced
different bytes on the wire. Add json.Deterministic(true) to the inbound
thinking marshal, the Gemini generationConfig and request, the Gemini response
boundaries and the request translator, and pin each seam with a repeated-call
test."
```

---

### Task 4: Validate the `providerThinking.mode` vocabulary (F6)

**Files:**
- Modify: `internal/providers/capabilities.go` (add the mode vocabulary next to `CanDisableThinking`)
- Modify: `internal/handlers/dashboard/settings.go` (`HandleUpdateSettings`)
- Modify: `internal/db/settings.go:326-335` (`GetSettings` read path)
- Modify: `internal/handlers/dashboard/settings_test.go`
- Modify: `internal/handlers/chat/thinking_wire.go:47-63` (use the shared validator)

**Interfaces:**
- Consumes: `internal/thinking`'s `EffortLevels` — but `internal/handlers/dashboard` must not import `internal/thinking`, so the vocabulary is declared once in `internal/providers` and `internal/thinking` keeps its own list.
- Produces:
  - `var ThinkingModes = []string{"auto", "on", "off", "minimal", "low", "medium", "high", "xhigh", "max"}` — `internal/providers/capabilities.go`.
  - `func ValidThinkingMode(mode string) bool` — `internal/providers/capabilities.go`.

- [ ] **Step 1: Write the failing dashboard test**

Add `TestHandleUpdateSettings_ProviderThinkingMode` to `internal/handlers/dashboard/settings_test.go`: a valid mode (`"high"`) is accepted and stored; an invalid mode (`"banana"`) is rejected with HTTP 400; a mode of `"on"`/`"off"`/`"auto"` is accepted. Follow the shape of the existing `TestHandleUpdateSettings_PasswordChange` test in the same file.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/handlers/dashboard/ -run TestHandleUpdateSettings_ProviderThinkingMode -v`
Expected: FAIL — the invalid mode is currently accepted.

- [ ] **Step 3: Declare the vocabulary**

In `internal/providers/capabilities.go`, add `ThinkingModes` and `ValidThinkingMode` exactly as specified in the Interfaces block. `ValidThinkingMode` compares case-insensitively after trimming.

- [ ] **Step 4: Reject an invalid mode on write**

In `internal/handlers/dashboard/settings.go`, in `HandleUpdateSettings`, after the `protectedSettingKeys` check and before `h.Repo.UpdateSettingsRaw(updates)`, validate `updates["providerThinking"]`: it must be a `map[string]any` whose every entry is a `map[string]any` with a `"mode"` string that `providers.ValidThinkingMode` accepts. On any violation, respond `handlerutil.WriteJSONError(w, http.StatusBadRequest, "providerThinking mode must be one of auto, on, off, minimal, low, medium, high, xhigh, max")` and return. Add the `9router/proxy/internal/providers` import to the file if it is not already there.

- [ ] **Step 5: Drop an invalid mode on read**

In `internal/db/settings.go`, in the `providerThinking` read block (around line 326), keep only entries whose `Mode` passes `providers.ValidThinkingMode`; drop the rest. `internal/db` already imports `internal/providers`? If not, add the import.

- [ ] **Step 6: Use the validator where the override is applied**

In `internal/handlers/chat/thinking_wire.go`, `injectProviderThinking` currently treats any unrecognised mode as a reasoning level. Change the `default:` branch to apply the mode only when `providers.ValidThinkingMode(mode)` is true; otherwise return without writing `reasoning_effort`.

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/handlers/dashboard/ ./internal/db/ ./internal/handlers/chat/ -count=1`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
rtk git add internal/providers/capabilities.go internal/handlers/dashboard/settings.go internal/handlers/dashboard/settings_test.go internal/db/settings.go internal/handlers/chat/thinking_wire.go
rtk git commit -m "fix(settings): validate the providerThinking mode vocabulary

Any non-empty mode was stored and written straight into reasoning_effort, so a
hand-edited settings blob could put an invalid effort on the wire. Declare the
allowed modes once, reject them on write and drop them on read."
```

---

### Task 5: Bound the effort vocabulary so an unknown level never reaches a provider (F7)

**Files:**
- Modify: `internal/thinking/levels.go:72-81` (`EffortToThinkingLevel`), `:86` (`NormalizeOpenAILevel`)
- Modify: `internal/thinking/levels_test.go`

**Interfaces:**
- Consumes: `internal/thinking.EffortLevels` — `[]string{"minimal","low","medium","high","xhigh","max"}` (`internal/thinking/levels.go:10`).
- Produces: no new exported symbol. `EffortToThinkingLevel` maps `xhigh`, `max` and `ultra` to `"high"` and any unknown level to `"high"`; `NormalizeOpenAILevel` maps any level outside `EffortLevels` to `"medium"`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/thinking/levels_test.go`:

- `TestEffortToThinkingLevelUnknown` — table over `"ultra" → "high"`, `"xhigh" → "high"`, `"max" → "high"`, `"banana" → "high"`, `"minimal" → "minimal"`, `"high" → "high"`.
- `TestNormalizeOpenAILevelUnknown` — table over `"banana" → "medium"`, `"" → "medium"`, `"high" → "high"`, `"minimal" → "minimal"`, and `"max"` with a `supported` list containing `"max"` → `"max"`, `"max"` with a `supported` list not containing it → `"xhigh"`, `"ultra"` with `"max"` supported → `"max"`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/thinking/ -run 'TestEffortToThinkingLevelUnknown|TestNormalizeOpenAILevelUnknown' -v`
Expected: FAIL — `"ultra"` returns `"ultra"` and `"banana"` returns `"banana"`.

- [ ] **Step 3: Bound the Gemini level**

In `internal/thinking/levels.go`, change `EffortToThinkingLevel`'s switch to map `none|off` → `"minimal"`, `xhigh|max|ultra` → `"high"`, `minimal|low|medium|high` → the value itself, and anything else → `"high"`.

- [ ] **Step 4: Bound the OpenAI level**

In `internal/thinking/levels.go`, in `NormalizeOpenAILevel`, after the existing `max`/`ultra` handling, return `"medium"` when the level is not one of `EffortLevels` (case-insensitive, trimmed). Keep the existing `max`/`ultra` behaviour intact: `"ultra"` falls back to `"max"` when the model advertises `"max"`, else to `"xhigh"`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/thinking/ -count=1`
Expected: PASS.

- [ ] **Step 6: Pin the writers**

Add `TestWriterLevelsStayInsideTheProviderVocabulary` to `internal/thinking/levels_test.go`: for the formats `openai`, `openai-responses`, `gemini-level`, `kimi` and `deepseek`, apply an intent with `Level: "ultra"` and one with `Level: "banana"`, and assert the emitted level is never the raw client string and is always one of:

- `openai`, `openai-responses`: a member of `EffortLevels` (`minimal|low|medium|high|xhigh|max`)
- `gemini-level`: one of `minimal|low|medium|high`
- `kimi`, `deepseek`: one of `low|medium|high|max`, or absent

`gemini-budget`, `qwen`, `minimax` and `hunyuan` emit a number or a boolean rather than a level string, so they are out of scope for this test.

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/thinking/ -run TestWriterLevelsStayInsideTheProviderVocabulary -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
rtk git add internal/thinking/levels.go internal/thinking/levels_test.go
rtk git commit -m "fix(thinking): keep an unknown effort level off the wire

A client-supplied reasoning_effort reached the provider verbatim, so
reasoning_effort:ultra became thinkingLevel:ultra and any other string became
an invalid enum. Bound both level mappers to the known vocabularies."
```

---

### Task 6: Scan the request body with the bytes package (F8)

**Files:**
- Modify: `internal/thinking/format.go` (`bodyModelHasSuffix` and the hand-rolled byte helpers)
- Create: `internal/thinking/scan_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: no new exported symbol. `bodyModelHasSuffix(body []byte) bool` keeps its signature; `indexOfBytes`, `indexOfByte`, `bytesEqual`, `containsByte`, `trimLeftByte` and `trimRightByte` are deleted in favour of the `bytes` package.

**Note:** the original finding was that this scan was quadratic. That was wrong — the scan walks forward from one occurrence to the next, so it was always linear; the cost was the hand-rolled byte comparison, not the number of occurrences. This task is a constant-factor cleanup, not a vulnerability fix.

- [ ] **Step 1: Write the tests**

Create `internal/thinking/scan_test.go` with two tests:

- `TestBodyModelHasSuffixStaysFastOnALargeBody` — build a ~1.1 MiB body of 80 000 `{"model":123}` entries (so the literal `"model"` occurs 80 000 times), assert `bodyModelHasSuffix(body)` is false, and assert the call completes within 2 seconds. The bound is a regression guard with a wide margin, not a timing assertion about machine speed.
- `TestBodyModelHasSuffixFindsTheSuffix` — a table over `{"model":"m(high)"}` (true), `{"model" : "m(high)"}` (true), `{"model":"m"}` (false), `{"modelId":"m(high)"}` (true), `{"model_id":"m(high)"}` (true), `{"note":"see (docs)"}` (false), `{"x":"model","model":"m"}` (false) and `{"model":"m(high` (false).

- [ ] **Step 2: Run the tests**

Run: `go test ./internal/thinking/ -run TestBodyModelHasSuffix -v -count=1`
Expected: the suffix table passes on the old code; the timing guard passes as well, because the old scan was already linear.

- [ ] **Step 3: Rewrite the scan**

In `internal/thinking/format.go`, rewrite `bodyModelHasSuffix` to walk the body once per key with `bytes.Index`: for each occurrence of `"model"`, `"modelId"` or `"model_id"`, skip the whitespace and the colon that separate a key from its value, read the quoted value, and report whether it carries a `(…)` suffix. Advance the cursor past the value so the next occurrence is found from there.

Delete `indexOfBytes`, `indexOfByte`, `bytesEqual`, `containsByte`, `trimLeftByte` and `trimRightByte`, and add a `skipJSONSpace(body []byte, i int) int` helper for the whitespace-and-colon skip.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/thinking/ -run TestBodyModelHasSuffix -v -count=1`
Expected: PASS, and the timing guard shows the new scan is faster than the old one on the same fixture.

- [ ] **Step 5: Run the package**

Run: `go test ./internal/thinking/ -count=1`
Expected: PASS — the existing suffix tests still hold.

- [ ] **Step 6: Commit**

```bash
rtk git add internal/thinking/format.go internal/thinking/scan_test.go
rtk git commit -m "perf(thinking): scan the request body with the bytes package

The scan compared bytes by hand on every step of the model-key walk. Use
bytes.Index and bytes.IndexByte, and drop the hand-rolled helpers."
```

---

### Task 7: Match the OpenAI filter to upstream and split it (F9, F10, F13)

**Files:**
- Create: `internal/translator/openai_filter_messages.go`
- Modify: `internal/translator/openai_filter.go`
- Modify: `internal/handlers/chat/fallback.go` (the `FilterToOpenAIFormatJSON` call)
- Modify: `internal/translator/openai_filter_test.go`

**Interfaces:**
- Consumes: the upstream reference file (fetched in Step 1).
- Produces:
  - `type FilterOptions struct { PreserveCacheControl bool }` — `internal/translator/openai_filter.go`.
  - `func FilterToOpenAIFormat(body map[string]any, opts FilterOptions) map[string]any` — the existing function gains the options parameter.
  - `func FilterToOpenAIFormatJSON(body []byte, opts FilterOptions) []byte` — likewise.
  - In `internal/translator/openai_filter_messages.go`: `func filterMessages(msgs []any, opts FilterOptions) []any`, `func filterContentBlocks(parts []any, opts FilterOptions) []any`, `func normalizeTools(tools []any) []any`, `func normalizeToolChoice(body map[string]any)`.

- [ ] **Step 1: Fetch the upstream reference and decide**

Run:
```bash
curl -sS https://raw.githubusercontent.com/decolua/9router/main/open-sse/translator/formats/openai.js -o /tmp/upstream-openai.js
curl -sS https://raw.githubusercontent.com/decolua/9router/main/open-sse/translator/index.js -o /tmp/upstream-index.js
```
Read both files and record two answers in the commit message of this task:

- **F9** — does upstream's `filterToOpenAIFormat` filter the content of an assistant message that carries `tool_calls`, and does it delete a message-level `reasoning_content`? If upstream strips them, implement the strip and update `internal/translator/openai_filter_test.go:145-157` (the subtest named "assistant tool_calls shortcut keeps thinking blocks") to assert the opposite. If upstream keeps them, keep the behaviour and the test, and replace the subtest's name with one that states the upstream reference.
- **F10** — what is upstream's `preserveCacheControl` condition, and which providers are registered for it? Port that list verbatim into `FilterOptions`.

If the fetch fails (no network), do not guess: leave the behaviour unchanged, keep the existing test, and record "upstream reference unavailable — parity unverified" in the `.changes/` fragment. Note that the same fetch is required by Task 11.

- [ ] **Step 2: Add the options parameter**

In `internal/translator/openai_filter.go`, add the `FilterOptions` type and give both exported functions the extra parameter. When `opts.PreserveCacheControl` is false (the default), `stripBlock` keeps its current behaviour of deleting both `signature` and `cache_control`. When it is true, `stripBlock` deletes `signature` only.

- [ ] **Step 3: Update the call site**

In `internal/handlers/chat/fallback.go`, the existing call becomes `translator.FilterToOpenAIFormatJSON(pipedBody, translator.FilterOptions{PreserveCacheControl: <upstream's condition>})`. If upstream registers the quirk per provider, derive the boolean from the provider id using the ported list.

- [ ] **Step 4: Split the function**

Move the message handling out of `internal/translator/openai_filter.go` into `internal/translator/openai_filter_messages.go` as the four helpers named in the Interfaces block, and reduce `FilterToOpenAIFormat` to a body that reads the `messages` array and calls them. Keep the behaviour identical except for the two decisions taken in Step 1.

- [ ] **Step 5: Update the tests**

In `internal/translator/openai_filter_test.go`, update the helper that calls the filter to pass `FilterOptions{}`, and add a case that passes `FilterOptions{PreserveCacheControl: true}` and asserts `cache_control` survives while `signature` is removed. Apply the F9 decision from Step 1 to the "assistant tool_calls shortcut" subtest. Update `TestFilterToOpenAIFormatJSONDeterministic` in `internal/translator/openai_filter_determinism_test.go` (added in Task 3) to pass the new options argument.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/translator/ ./internal/handlers/chat/ -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
rtk git add internal/translator/openai_filter.go internal/translator/openai_filter_messages.go internal/translator/openai_filter_test.go internal/handlers/chat/fallback.go
rtk git commit -m "refactor(translator): split the OpenAI filter and match upstream's cache-control quirk

F9: <the upstream answer recorded in Step 1>.
F10: upstream preserves cache_control for providers registered with the
preserveCacheControl quirk; carry that as an explicit option instead of
dropping the field unconditionally."
```

---

### Task 8: Make the Gemini test helper fail loudly (F11, F12)

**Files:**
- Modify: `internal/translator/gemini_thinking_test.go` (`geminiMaxTokens`)
- Modify: `internal/translator/gemini.go` (the capability lookup comment)

**Interfaces:**
- Consumes: `providers.GetCapabilitiesForModel(provider, model) Capabilities`.
- Produces: no signature change. `TranslateOpenAIToGemini(openaiBody []byte) ([]byte, error)` keeps its signature.

**Note:** F12 (the capability lookup losing the provider context) turned out not to be a defect. The capabilities pattern table carries no provider scope, the lookup strips the vendor prefix from the model id, and the existing parity test already pins the resolution (gemini-2.5 → budget, gemini-3 → level). The task documents the contract instead of changing the signature, and no call site changes.

- [ ] **Step 1: Fix the test helper**

In `internal/translator/gemini_thinking_test.go`, change `geminiMaxTokens` to fail loudly: `t.Fatalf` when `generationConfig` is missing or is not a `map[string]any`, and when `maxOutputTokens` is missing or is not a `float64`. It returns the value only after both assertions pass.

- [ ] **Step 2: Run the Gemini tests**

Run: `go test ./internal/translator/ -run TestTranslateOpenAIToGemini -v`
Expected: PASS, and the helper now reports a wrong-shaped body instead of returning 0.

- [ ] **Step 3: Document the capability contract**

In `internal/translator/gemini.go`, replace the comment above the capability lookup with the precise contract: the capability rows are model patterns, the pattern table carries no provider scope, so the lookup is provider-agnostic by design and a vendor prefix on the model id is stripped.

- [ ] **Step 4: Pin the output-token budget**

Add `TestTranslateOpenAIToGeminiWritesMaxOutputTokens` to `internal/translator/gemini_thinking_test.go`: translate a gemini-2.5 request with `reasoning_effort:"high"` and assert the helper reads a positive `maxOutputTokens`. With the old helper a missing field read as 0, so the assertion could not fail.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/translator/ ./internal/proxy/executor/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
rtk git add internal/translator/gemini.go internal/translator/gemini_thinking_test.go
rtk git commit -m "test(translator): make the Gemini helper fail loudly instead of reporting zero

The helper turned a missing or wrong-shaped generationConfig into a zero, so a
translator regression that dropped the output-token budget could not fail the
test that reads it."
```

---

### Task 9: Repair the stray character and the numeric overflow paths (F14, F15)

**Files:**
- Modify: `internal/translator/gemini.go:535`
- Modify: `internal/thinking/suffix.go:36-38`
- Modify: `internal/thinking/intent.go:188-214` (`numberField`)
- Modify: `internal/thinking/intent_test.go` (or the file Task 10 splits it into)

**Interfaces:**
- Consumes: nothing.
- Produces: no new exported symbol. `numberField` rejects a value outside `[0, 1<<30]`; `ParseSuffix` returns no intent for a numeric suffix that does not fit in an `int`.

- [ ] **Step 1: Fix the stray character**

In `internal/translator/gemini.go:535`, restore the ASCII closing backtick: the line must end `...${index}\` in` — the U+201C character currently there is replaced by a backtick.

- [ ] **Step 2: Write the failing tests**

Add to `internal/thinking/intent_test.go`:

- `TestNumberFieldRejectsOutOfRange` — `numberField(float64(1e30))` reports not-ok; `numberField(float64(-5))` reports not-ok; `numberField(float64(1024))` reports ok with 1024.
- `TestParseSuffixRejectsAnOverflowingBudget` — `ParseSuffix("model(99999999999999999999)")` returns the cleaned model and a nil intent; `ParseSuffix("model(8192)")` returns a `ModeBudget` intent with `Budget == 8192`.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/thinking/ -run 'TestNumberFieldRejectsOutOfRange|TestParseSuffixRejectsAnOverflowingBudget' -v`
Expected: FAIL — `1e30` currently converts to a garbage `int`, and the overflowing suffix yields `Budget = MaxInt64`.

- [ ] **Step 4: Bound the number conversion**

In `internal/thinking/intent.go`, in `numberField`, reject any value outside `[0, 1<<30]` (a token ceiling above any real model budget) for the `float64`, `float32`, `int`, `int64` and `int32` cases, and report not-ok. A `string` case keeps using `strconv.Atoi` but must also apply the same bound.

- [ ] **Step 5: Handle the suffix parse error**

In `internal/thinking/suffix.go`, change the numeric branch to:

```go
case allDigits(raw):
    n, err := strconv.Atoi(raw)
    if err != nil {
        return clean, nil
    }
    return clean, &Intent{Mode: ModeBudget, Budget: n}
```

The suffix is still stripped (`clean` is returned) — the fragment already promises "unknown values still strip" — but no intent is produced for a value that does not fit.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/thinking/ -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
rtk git add internal/translator/gemini.go internal/thinking/suffix.go internal/thinking/intent.go internal/thinking/intent_test.go
rtk git commit -m "fix(thinking): reject out-of-range budgets and restore a stray character

A budget beyond the int range converted to a garbage value and a numeric model
suffix overflowed strconv.Atoi into MaxInt64; both now report no intent. Also
restores the closing backtick a parity comment lost."
```

---

### Task 10: Restore the lost ZAI coverage and split the oversized test files (F16, F17)

**Files:**
- Create: `internal/proxy/executor/zai_noop_test.go`
- Split: `internal/thinking/intent_test.go` (528) → `internal/thinking/intent_test.go` + `internal/thinking/intent_dialect_test.go`
- Split: `internal/thinking/format_test.go` (451) → `internal/thinking/format_test.go` + `internal/thinking/format_writers_test.go`
- Split: `internal/handlers/chat/thinking_wire_test.go` (314) → `internal/handlers/chat/thinking_wire_test.go` + `internal/handlers/chat/thinking_wire_determinism_test.go`

**Interfaces:**
- Consumes: the deleted test body, recoverable with `rtk git show origin/main:internal/proxy/executor/zai_test.go`.
- Produces: no new exported symbol. Every resulting file is <= 300 LoC.

- [ ] **Step 1: Port the deleted executor-level test**

Run: `rtk git show origin/main:internal/proxy/executor/zai_test.go`
Read `TestApplyZaiThinking_LeavesUnrelatedBodiesAlone` and create `internal/proxy/executor/zai_noop_test.go` containing that test, renamed `TestForwardZai_LeavesUnrelatedBodiesAlone`, calling the function that forwards to Z.ai. Port only that test — the 11 transformation cases already live in `internal/thinking/format_test.go` as `TestApplyZaiPorted`.

- [ ] **Step 2: Run it**

Run: `go test ./internal/proxy/executor/ -run TestForwardZai_LeavesUnrelatedBodiesAlone -v`
Expected: PASS. If it fails, the executor mutates a body it should not — fix `internal/proxy/executor/zai.go` before continuing.

- [ ] **Step 3: Split `internal/thinking/intent_test.go`**

Move the display-dialect and strip-dialect tables (`TestOpenAIThinkingDisplayDialects`, `TestStripDialectKeys`, `TestNormalizeThinkingConfigDialectKeys`) into a new `internal/thinking/intent_dialect_test.go`. Keep `TestExtract`, `TestCaptureThinking`, `TestParseSuffix`, `TestStrip` and `TestNormalizeThinkingConfig` in `intent_test.go`.

- [ ] **Step 4: Split `internal/thinking/format_test.go`**

Move the per-writer cases (`TestApplyClaudeAdaptive`, `TestApplyClaudeBudget`, `TestApplyOpenAI`, `TestApplyZai`, `TestApplyQwenDeepseekKimiMinimaxHunyuan`, `TestApplyTokenRouter`, `TestApplyCommandcode`, `TestApplyDisplay`, `TestApplyZaiPorted`) into a new `internal/thinking/format_writers_test.go`. Keep the rest in `format_test.go`.

- [ ] **Step 5: Split `internal/handlers/chat/thinking_wire_test.go`**

Move the determinism test added in Task 3 into a new `internal/handlers/chat/thinking_wire_determinism_test.go`.

- [ ] **Step 6: Check the file sizes**

Run: `wc -l internal/thinking/intent_test.go internal/thinking/intent_dialect_test.go internal/thinking/format_test.go internal/thinking/format_writers_test.go internal/handlers/chat/thinking_wire_test.go internal/handlers/chat/thinking_wire_determinism_test.go`
Expected: every file <= 300 lines.

- [ ] **Step 7: Run the tests**

Run: `go test ./internal/thinking/ ./internal/handlers/chat/ ./internal/proxy/executor/ -count=1`
Expected: PASS — a split must not lose a case: the total number of test functions across the split files must equal the number before the split.

- [ ] **Step 8: Commit**

```bash
rtk git add internal/proxy/executor/zai_noop_test.go internal/thinking/intent_test.go internal/thinking/intent_dialect_test.go internal/thinking/format_test.go internal/thinking/format_writers_test.go internal/handlers/chat/thinking_wire_test.go internal/handlers/chat/thinking_wire_determinism_test.go
rtk git commit -m "test(thinking): restore the ZAI no-op coverage and split the oversized test files

The executor-level ZAI no-op case was lost with the deleted zai_test.go, and
three new test files exceeded the project size threshold."
```

---

### Task 11: Match the CommandCode request conversion to upstream (F18)

**Files:**
- Modify: `internal/proxy/executor/commandcode_request.go:207-214` (role handling), `:230-240` (nullish fields)
- Modify: `internal/proxy/executor/providers.go:425-446` (the body assembly that copies the client keys)
- Modify: `internal/proxy/executor/commandcode_request_test.go`

**Interfaces:**
- Consumes: the upstream reference file, fetched in Task 7 Step 1 (`https://raw.githubusercontent.com/decolua/9router/main/open-sse/translator/request/openai-to-commandcode.js`).
- Produces: no new exported symbol.

- [ ] **Step 1: Read the upstream reference**

Run: `curl -sS https://raw.githubusercontent.com/decolua/9router/main/open-sse/translator/request/openai-to-commandcode.js -o /tmp/upstream-openai-to-commandcode.js`
Read `convertMessages` and the `params` assembly. Record two answers: how a `developer` role turn is emitted, and how a `null` value for `max_tokens`/`temperature`/`top_p` is treated.

- [ ] **Step 2: Write the failing tests**

Add to `internal/proxy/executor/commandcode_request_test.go`:

- `TestCommandcodeDeveloperRole` — a body with a `developer` turn; assert the emitted `params` matches upstream's role placement.
- `TestCommandcodeNullFieldsAreAbsent` — a body with `"max_tokens": null`, `"temperature": null`, `"top_p": null`; assert the emitted `params` does not carry those keys.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/proxy/executor/ -run 'TestCommandcodeDeveloperRole|TestCommandcodeNullFieldsAreAbsent' -v`
Expected: FAIL.

- [ ] **Step 4: Implement the upstream behaviour**

In `internal/proxy/executor/commandcode_request.go`, emit a `developer` turn as upstream does (the upstream answer from Step 1). In `internal/proxy/executor/providers.go:425-446` and `internal/proxy/executor/commandcode_request.go:230-240`, treat a `nil` value for `max_tokens`, `temperature` and `top_p` as absent — skip the key instead of writing a JSON `null`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/proxy/executor/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
rtk git add internal/proxy/executor/commandcode_request.go internal/proxy/executor/commandcode_request_test.go internal/proxy/executor/providers.go
rtk git commit -m "fix(executor): match the CommandCode request conversion to upstream

A developer turn was emitted as params.system where upstream keeps only system
there, and a null max_tokens/temperature/top_p reached the provider instead of
being treated as absent."
```

---

### Task 12: Normalize the `mimo-free` combo lane (F20)

**Files:**
- Modify: `internal/handlers/chat/combo.go:611` (the `h.MimoFreeChat` call)
- Create: `internal/integration/mimo_free_reasoning_test.go`

**Interfaces:**
- Consumes: `thinking.ClampDisableToMinimalJSON` (Task 1), `providers.GetCapabilitiesForModel`.
- Produces: no new exported symbol.

- [ ] **Step 1: Write the failing integration test**

Create `internal/integration/mimo_free_reasoning_test.go` (build tag `integration`): a combo whose only member is a `mimo-free` model, a `mimo-free` connection to a fake upstream, and a client request carrying `reasoning_effort: "none"`. Assert the upstream body contains `"minimal"` and not `"none"`. Use the combo helper the other combo integration tests use — copy the call shape from the nearest existing combo test in `internal/integration/`, and use `env.AddCombo(t, ...)` and `env.AddConnection(t, "mimo-free-1", "mimo-free", "mimo-free", up, "test-key")`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags=integration ./internal/integration/ -run TestMimoFreeComboLaneClampsDisabledReasoning -v -count=1`
Expected: FAIL — the upstream body still carries `"none"`.

- [ ] **Step 3: Clamp the lane**

In `internal/handlers/chat/combo.go`, immediately before the `h.MimoFreeChat(ctx, cw, upstreamJSON, isStream, comboMetrics)` call, apply the clamp:

```go
upstreamJSON = thinking.ClampDisableToMinimalJSON(upstreamJSON, providers.GetCapabilitiesForModel("mimo-free", modelInfo.Model))
```

`internal/handlers/chat/combo.go` needs the `internal/providers` and `internal/thinking` imports.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test -tags=integration ./internal/integration/ -run TestMimoFreeComboLaneClampsDisabledReasoning -v -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/handlers/chat/combo.go internal/integration/mimo_free_reasoning_test.go
rtk git commit -m "fix(chat): clamp a disabled-reasoning request on the mimo-free combo lane

The combo path calls MimoFreeChat directly, so neither the thinking chokepoint
nor the mandatory-reasoning clamp ran there."
```

---

### Task 13: Changelog fragments and the full gate

**Files:**
- Create: `.changes/<pr-number>-thinking-normalization-fixes.md`
- Modify: `.changes/1005-thinking-normalization-parity.md` (the "at both seams" claim is now wrong)

**Interfaces:**
- Consumes: everything the earlier tasks produced.
- Produces: the PR-ready state.

- [ ] **Step 1: Write the fragment**

Create `.changes/<pr-number>-thinking-normalization-fixes.md` with a `### Fixed` section covering: the Responses-native clamp (F1), the Claude disable dialect (F2), the deterministic wire boundaries (F3/F4), the `providerThinking` validation (F6), the bounded effort vocabulary (F7), the linear body scan (F8), the OpenAI filter parity and cache-control quirk (F9/F10), the Gemini capability context (F12), the numeric bounds (F15), and the CommandCode parity (F18). State plainly which upstream behaviours were confirmed by fetching the upstream reference and which could not be verified (no network).

- [ ] **Step 2: Correct the existing fragment**

In `.changes/1005-thinking-normalization-parity.md`, the bullet claiming "Deterministic serialisation of the outgoing thinking-normalised body at both seams" now describes more than two seams. Update it to name the seams that are deterministic.

- [ ] **Step 3: Run the full gate**

Run:
```bash
go build ./... && \
go vet ./... && \
go test ./... -count=1 && \
go test -tags=integration ./internal/integration/... -count=1
```
Expected: build 0 (spec V1), vet 0 (spec V2), unit suite green except the pre-existing DNS-dependent `TestForwardQoder_UnknownModelNamesTheModel` (spec V3), integration suite green (spec V4).

- [ ] **Step 4: Run the determinism sweep**

Run: `go test ./internal/thinking/ ./internal/translator/ ./internal/handlers/chat/ -count=100`
Expected: PASS, no flakes.

- [ ] **Step 5: Prove the PR touches no changelog file and only the intended files**

Run:
```bash
{ rtk git diff --name-only HEAD; rtk git diff --name-only origin/main...HEAD; \
  rtk git status --porcelain | cut -c4-; } | rtk grep -qx CHANGELOG.md \
  && echo "VIOLATION: CHANGELOG.md must not appear in a PR diff"
rtk git diff --name-only origin/main...HEAD
```
Expected: no `VIOLATION` line; the file list contains only `internal/**`, `.changes/**`, `.gitignore` and `docs/superpowers/**` (spec V5, V6).

- [ ] **Step 6: Commit**

```bash
rtk git add .changes/
rtk git commit -m "docs(changes): record the thinking-normalisation review fixes"
```

- [ ] **Step 7: Open the PR**

```bash
rtk git push -u origin fix/thinking-normalization
gh pr create --repo luqman-v1/9router-go --base main --head fix/thinking-normalization \
  --title "fix(thinking): close the reasoning-normalisation review findings" \
  --body "Closes the findings of the 2026-10-21 review of fix/thinking-normalization. Blockers: the mandatory-reasoning clamp now runs on the Responses-native lane, and a Claude client's thinking.type=disabled survives the request conversion. Also: deterministic marshalling at every map-to-bytes wire boundary with direct seam tests, a bounded effort vocabulary, a validated providerThinking mode, a linear request-body scan, upstream-matched OpenAI filter and CommandCode parity, and the mimo-free combo lane. See .changes/ for the full list."
```
Expected: a PR against `main` whose changed-file list matches Step 5.
