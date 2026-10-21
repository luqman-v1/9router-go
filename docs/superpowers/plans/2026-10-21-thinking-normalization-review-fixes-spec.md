# Spec — Fix all findings from the `fix/thinking-normalization` review

Branch: `fix/thinking-normalization` (HEAD `32a648c`), base `main`.
Source of truth: the code review of `git diff main...HEAD` performed 2026-10-21
(findings F1–F20 below). Every requirement here was verified against the
source in that review; line numbers are the ones cited there.

## Goal

Every finding in the review is either fixed in code with a regression test, or
closed with evidence that the current behaviour matches upstream and the
finding is recorded as a documented divergence. No finding is closed by
deleting or weakening a test.

## Blockers (the branch's own headline fixes are incomplete)

**F1 — The mandatory-reasoning clamp does not reach the Responses-native lane.**
`internal/handlers/chat/fallback.go:481-490` runs `thinking.ApplyJSON` only
when `!executor.UpstreamSpeaksResponses(provider, wireModel, providerCfg)`.
That predicate is unconditionally true for provider `muse`
(`internal/proxy/executor/responses_bridge.go:59-63`) and true for
`opencode`/`opencode-go`/`opencode-zen` on Responses-native models. On those
lanes `reasoning_effort:"none"` still reaches the provider:
`normalizeMuseSparkResponsesBody` (`internal/proxy/executor/providers.go:1097`,
called at `:546` and `:1172`) forwards `"none"` as `reasoning.effort:"none"`,
and `applyZenResponsesReasoning`
(`internal/proxy/executor/opencode_zen.go:443`, called at `:397`) deletes
`reasoning` entirely. The provider still answers *"Reasoning is mandatory for
this endpoint and cannot be disabled"* — the exact error
`.changes/227-muse-spark-contributor-mandatory-reasoning.md` claims to fix.
The `ThinkingCanDisable:new(false)` rows added at
`internal/providers/capabilities.go:235-238` are never consulted on this path.

**F2 — A Claude client's `thinking:{type:"disabled"}` is dropped before the
chokepoint that is meant to capture it.** `TranslateClaudeToOpenAI`
(`internal/translator/request.go:436-449`) handles `creq.Thinking.Type` values
`"enabled"` and `"adaptive"` only — there is no `"disabled"` branch — so the
converted body carries no reasoning dialect and
`prepareInboundThinking` (`internal/handlers/chat/thinking_wire.go:19`, called
from `fallback.go:278`) captures nothing. The branch's stated design ("intent
captured pre-conversion") does not hold on the Claude endpoint path.

## Major

**F3 — `internal/handlers/chat/thinking_wire.go:37` marshals without
`json.Deterministic(true)`.** The inbound half of the pipeline is not
byte-stable. json/v2's default `json.Marshal` over a `map[string]any` is
order-unstable (verified empirically: the same map marshalled twice produced
different key orders on 2026-10-21); only `json.Deterministic(true)` sorts.

**F4 — The same gap at other map→bytes wire boundaries.**
`internal/translator/gemini.go:458` (the `genConfig` map the branch now fills
with `genConfig["thinkingConfig"] = tc`), `gemini.go:465`, `gemini.go:581`,
`gemini.go:669`, `gemini.go:737`, `gemini.go:871`,
`internal/translator/request.go:631`, `:658`, `:851`, `:864`.
`.changes/1005-thinking-normalization-parity.md` says "at both seams"; there
are at least four.

**F5 — The deterministic seam has no direct unit test.**
`FilterToOpenAIFormatJSON` — the serializer commit `b68f79e` made
deterministic — has zero references in any test file. The only determinism
unit test is `internal/thinking/determinism_test.go` for `ApplyJSON`. A
regression to `json.Marshal(m)` would pass CI.

**F6 — `providerThinking.mode` is unvalidated.**
`internal/db/settings.go:326-335` stores any non-empty string and
`internal/handlers/chat/thinking_wire.go:47-63` writes an unrecognised mode
straight into `reasoning_effort`. The settings blob is hand-editable through
backup import, so `mode:"banana"` becomes `reasoning_effort:"banana"` and a
provider 400.

**F7 — The effort vocabulary is unbounded.**
`internal/thinking/intent.go:176-186` accepts any string as a level.
`internal/thinking/levels.go:72-81` `EffortToThinkingLevel` returns unknown
values verbatim, so `reasoning_effort:"ultra"` becomes `thinkingLevel:"ultra"`
(Gemini's enum is `minimal|low|medium|high`) and `"banana"` becomes
`thinkingLevel:"banana"`. `NormalizeOpenAILevel`
(`internal/thinking/levels.go:86`) likewise passes unknown levels through.

**F8 — The request-body scan is slower than it needs to be (the quadratic claim
was a false positive).** `internal/thinking/format.go` scanned the body with
hand-rolled `indexOfBytes`/`indexOfByte`/`bytesEqual` helpers instead of the
`bytes` package. The scan walks forward from one occurrence to the next, so it
was always linear — the cost was a constant factor, not a quadratic blowup.
The rewrite keeps the scan linear, uses `bytes.Index`, and drops the
hand-rolled helpers.

**F9 — Assistant-with-`tool_calls` and `role:"tool"` messages bypass the
OpenAI filter.** `internal/translator/openai_filter.go:52-62` appends both
unfiltered, so their content blocks skip the thinking-block strip *and*
`stripBlock` (signature/`cache_control`). A `reasoning_content` field or
thinking block on such a message reaches providers that reject it. The
behaviour is pinned by `internal/translator/openai_filter_test.go:145-157`
("assistant tool_calls shortcut keeps thinking blocks"). Upstream parity is
unresolved: the upstream clone is not present on this machine.

**F10 — `cache_control` is dropped unconditionally.**
`internal/translator/openai_filter.go:249-254` (documented at `:31-34`).
Upstream preserves it for registered quirks — `open-sse/translator/formats/openai.js:9-16`
and `open-sse/translator/index.js:143-145` — for providers such as
DashScope/alicode. The divergence is documented in code but not in the
`.changes/` fragment, and it removes explicit prompt caching for those
providers.

**F11 — A test helper turns a real regression into a misleading failure.**
`internal/translator/gemini_thinking_test.go:22-34` `geminiMaxTokens` ignores
both the `generationConfig` and the `maxOutputTokens` type assertion and
returns `0` for a missing or wrong-shaped field, so any case expecting `0`
passes vacuously.

**F12 — The Gemini translator resolves capabilities globally.**
`internal/translator/gemini.go:429-450` calls
`providers.GetCapabilitiesForModel("", oreq.Model)` with an empty provider, so
a provider-scoped or custom-node capability row cannot be honoured on this
path.

**F13 — `FilterToOpenAIFormat` is one 248-pure-LOC branch-heavy function.**
`internal/translator/openai_filter.go:35-231` does role normalization, content
filtering, message pruning, tool conversion and tool-choice conversion in one
body — over the AGENTS.md §4.D complexity target, and the reason F9 is easy
to reintroduce.

## Minor

**F14** — `internal/translator/gemini.go:535` replaced a closing backtick with
U+201C in a parity comment.

**F15** — `internal/thinking/suffix.go:36-38` does `n, _ := strconv.Atoi(raw)`
and discards the range error, so `model(99999999999999999999)` yields
`Budget = MaxInt64`. `internal/thinking/intent.go:194-196` converts a
`float64` to `int` with no range check, so `budget_tokens: 1e30` is undefined
behaviour on amd64.

**F16** — `internal/proxy/executor/zai_test.go` (216 lines) was deleted. Its
11 transformation cases are ported to `internal/thinking/format_test.go:355`
(`TestApplyZaiPorted`), but the executor-level no-op case
(`TestApplyZaiThinking_LeavesUnrelatedBodiesAlone`) has no replacement, so
"the executor mutates an unrelated body" is no longer pinned.

**F17** — New test files exceed the AGENTS.md 400-LoC modularization
threshold: `internal/thinking/intent_test.go` (528),
`internal/thinking/format_test.go` (451),
`internal/handlers/chat/thinking_wire_test.go` (314).

**F18** — CommandCode parity gaps. `internal/proxy/executor/commandcode_request.go:207-214`
maps the `developer` role to `params.system`, where upstream treats only
`system` as `params.system` and `developer` as user content. Nulls reach
`max_tokens`/`temperature`/`top_p`
(`internal/proxy/executor/providers.go:425-446`,
`internal/proxy/executor/commandcode_request.go:230-240`) where upstream's JS
nullish semantics treat them as absent.

**F19** — Branch and PR scope. The branch is 23 ahead / **118 behind**
`origin/main`, and it also commits agent-local config (`.omo/omo.jsonc`,
512 lines; `.omp/settings.json`, 41 lines of personal model-role routing — no
secrets in either) and rewrites `AGENTS.md` (+205) and `CLAUDE.md` (+48). None
of that belongs in a feature PR, and the doc rewrites will conflict on merge.

**F20** — The `mimo-free` combo lane bypasses the chokepoint.
`internal/handlers/chat/combo.go:611` calls `h.MimoFreeChat` directly, so
neither `prepareInboundThinking` nor `ApplyJSON` runs. MiMo capability rows
(`internal/providers/capabilities.go:570-573`) declare `ThinkingCanDisable:
new(false)`, so a client asking for `reasoning_effort:"none"` on that lane
still forwards `"none"`.

## Verification requirements (every task must keep these green)

- V1 — `go build ./...` exits 0.
- V2 — `go vet ./...` exits 0.
- V3 — `go test ./... -count=1` passes, except the pre-existing
  `TestForwardQoder_UnknownModelNamesTheModel` (`internal/proxy/executor/qoder_body_test.go:321`),
  which needs DNS for `api3.qoder.sh` and is not in this branch's diff.
- V4 — `go test -tags=integration ./internal/integration/... -count=1` passes.
- V5 — No change to `CHANGELOG.md`; changelog entries live in
  `.changes/<pr>-<slug>.md` fragments (AGENTS.md §7.6).
- V6 — `git diff --name-only origin/main...HEAD` lists only files the tasks
  below name, plus the `.changes/` fragments.
