### feat(thinking): one unified reasoning-normalisation pass on every lane, plus the CommandCode tool parity and the review fixes it travels with

One unified pass (`internal/thinking`, a port of upstream
`open-sse/translator/concerns/thinkingUnified.js`) now owns reasoning on every
lane: it captures the client's intent once at the dispatch choke point, strips
every dialect, and writes the one the target wire accepts. Three review rounds
on this branch produced the fixes recorded below alongside it — the wire bodies
are deterministic, the operator's override is written in the client's dialect,
and an unknown effort level never reaches a provider. Upstream is the
specification throughout; the deliberate divergences are listed at the end.

**The unified pass**

- **Inbound half** captures the intent before the OpenAI converters rewrite the
  body (content-type strips, the `providerThinking` operator override —
  `on`/`off`/level, dashboard-written — and conversation-shape normalisation);
  the one exception is a `/v1/messages` client bound for a non-Claude upstream,
  where the Claude→OpenAI conversion runs first and the intent is read back
  from the converted body. **Outbound half** (`thinking.ApplyJSON` +
  `filterToOpenAIFormat`) runs last on the wire body, covering both 401-retry
  attempts. Non-reasoning models are stripped, a body the pass does not change
  is returned byte for byte, and unreachable formats are left untouched.
- The writer table (openai, openai-responses, claude-adaptive, claude-budget,
  gemini-level, gemini-budget, zai, qwen, kimi, deepseek, minimax, hunyuan,
  step, tokenrouter, commandcode, kiro) matches upstream `applyFormat`,
  including the `none`-on-cannot-disable clamp to `minimal`. `applyFormat` now
  dispatches to one writer per dialect instead of running a 200-line switch.
- The dialect is picked from the wire the request goes out on first, then the
  provider registry, then the model's capability. Upstream pins that rule for
  one provider by name (`muse` on a Responses endpoint); this port applies it to
  every provider on that lane, because a strict Responses API rejects a
  top-level `reasoning_effort` whatever the provider's default wire is. Without
  it the nine aggregators the registry records as `openai` sent
  `reasoning_effort` to a Responses endpoint and were answered `400 unknown
  parameter reasoning_effort`.
- **Registry `thinkingFormat` for all 12 upstream providers** (`tokenrouter`;
  nine `openai` aggregators — openrouter, siliconflow, cloudflare-ai, blackbox,
  venice, vercel-ai-gateway, codebuddy-cn, codebuddy-intl, iflow; the
  `minimax-code` pair as `claude-adaptive`; `tokenharbor` deliberately none)
  plus the kiro `strip:[image,audio]` model lists.
- **`(level)` model suffixes are honoured, and resolve before the model does.**
  `model(high)`, `model(none)`, `model(8192)`, `model(auto)` are stripped from
  the wire model and applied as the reasoning override (upstream `parseSuffix`:
  last paren wins, unknown values still strip). The suffix comes off the name
  before it is resolved, so it works on an alias and on a combo name, and the
  intent travels beside the request because the combo path replaces the body's
  model field with the member it is about to call. On the Responses-native lanes
  the pass runs too, with the target format taken from the wire the upstream
  speaks (`openai-responses`): the suffix is a request override that never
  enters the body, so a lane that skipped the pass dropped it silently and
  `muse/muse-spark-1.3-contributor(high)` reached the upstream with no reasoning
  field at all.
- **Gemini level/budget parity.** `reasoning_effort:high` is budget 24576 on
  gemini-2.5 (was 32000) and `thinkingLevel:high` on gemini-3, with `none`/`auto`
  handling and upstream output floors; Claude→OpenAI uses the upstream budget
  thresholds. A model without a `minimal` tier is clamped onto the lowest one it
  advertises — Gemini 4 and Argon answer 400 for `minimal`, which is what a
  disable request produced. The capability lookup's contract is documented: the
  rows are model patterns, the pattern table carries no provider scope, and a
  vendor prefix on the model id is stripped.
- **Z.ai dialect consolidation.** The `ForwardZai` executor rewrite is retired —
  it re-implemented the `zai` case and its `stripThinkingFields` would delete
  what the unified pass just wrote. All of its assertions (none→
  `enable_thinking:false` / minimal clamp, the `glm-5.2` and `glm-5.3` clamps
  carrying the effort level, low|high|max mapping, pre-5.2 drop, dialect
  precedence) are ported to the unified writer and asserted there.

**Provider-mandated reasoning**

- **cline-free/muse-spark contributor models** now clamp instead of forwarding
  a disable: `reasoning_effort:"none"` (or `thinking:{type:disabled}`, a `(none)`
  suffix, `providerThinking` off) reached `api.cline.bot` unchanged, which
  answers *"Reasoning is mandatory for this endpoint and cannot be disabled"*.
  The capability table declares the contributor family
  `ThinkingCanDisable: false` and the clamp lands them on `minimal`.
- **The clamp runs on the Responses-native lane too.** The pass never ran when
  the upstream speaks the Responses API — always the case for `muse` — so
  `reasoning_effort:none` was forwarded unchanged. It now happens at the
  dispatch choke point for both the Chat dialect (`reasoning_effort`) and the
  Responses dialect (`reasoning.effort`).
- **Every disable dialect the package reads is clamped**: `reasoning:{enabled:false}`
  and `thinking:{type:"disabled"}` used to pass through unclamped on a model
  that cannot disable reasoning.
- **A Claude client's `thinking:{type:"disabled"}` survives the conversion.**
  `TranslateClaudeToOpenAI` handled `enabled` and `adaptive` but not `disabled`,
  so the disable was dropped before the pass could capture it and the client
  silently received the provider default. It now maps to
  `reasoning_effort:"none"`, which the pipeline reads.
- **The mimo-free combo lane gets the clamp.** The combo path calls
  `MimoFreeChat` directly, so neither the pass nor the clamp ran there.

**CommandCode tool-call and tool-result parity** (upstream
`open-sse/translator/request/openai-to-commandcode.js`)

- An OpenAI `role:"tool"` turn becomes an AI-SDK `tool-result` block and an
  assistant `tool_calls` turn becomes `tool-call` blocks with parsed `input`;
  the Go executor passed both through untranslated, so CommandCode answered
  `400 BAD_REQUEST` for every request following a tool call
  (`z-ai/glm-5.3-flash`, `xiaomi/mimo-v2.6-flash`). The tool-result turn is
  emitted as `role:"tool"` (upstream `JS:120-123`): the provider validates
  `params.messages[*]` against a union of `{role:"user"|"assistant", …}` and
  `{role:"tool", content:[tool-result blocks]}`, so the block is only valid
  under `role:"tool"` — live-verified 2026-10-12, after an earlier revision read
  the header's `tool_result blocks (role=user)` note as the whole schema and
  kept the 400 alive. New `internal/proxy/executor/commandcode_request.go`
  ports `flattenText`, `toContentBlocks`, `safeParseJson` and `convertMessages`.
- `buildCommandcodeBody` builds `params` from a whitelist instead of copying
  every client key, and converts OpenAI `tools` through the existing
  `convertOpenAIToolsToClaude`. The native `{params:{…}}` envelope goes through
  the same whitelist, so a client can no longer set the model the provider is
  called with or pass any parameter the provider accepts on the operator's
  account. A null field is an absent field, the way upstream's nullish
  coalescing reads it: a null `max_tokens`, `temperature` or `top_p` falls back
  to the default instead of reaching the provider as a JSON null. The tool
  declarations are projected onto the three keys the schema defines.

**Determinism on the wire**

json/v2 does not order Go map keys, so the same logical request serialised as
different bytes on each call and every prefix cache missed. Every seam below
marshals with `json.Deterministic(true)`, and every one has a repeated-call test
that fails when the option is dropped (verified by removing it seam by seam and
running the suites):

- the inbound thinking marshal (`TestPrepareInboundThinkingDeterministic`), the
  unified pass itself (`TestApplyJSONDeterministic`) and the disable clamp;
- the OpenAI filter wire body (`TestFilterToOpenAIFormatJSONDeterministic`);
- the Gemini request (`TestTranslateOpenAIToGeminiDeterministic`) and both
  Gemini response boundaries, `TranslateGeminiResponseToOpenAI` and
  `TranslateGeminiChunkToOpenAI`, whose outputs carry a clock stamp the test
  normalises before comparing bytes
  (`TestTranslatorBoundariesAreDeterministic`);
- the request translator's Claude path, its passthrough sanitiser, the tool-type
  defaulting and both cache-anchor marshal sites
  (`TestTranslateClaudeToOpenAIToolChoiceIsDeterministic`,
  `TestTranslatorBoundariesAreDeterministic`);
- the CommandCode request bodies (both the caller-shaped `params` envelope and
  the flat one) and the muse-spark Responses body
  (`TestWireBodiesAreDeterministic`).
- the combo body, the mimo-free body and the OAuth cloak also re-marshalled
  without the option; they carry it now, and the integration suite pins the
  forwarded body byte-stable across identical requests
  (`TestForwardedBodyByteStableAcrossIdenticalRequests`,
  `TestAnthropicLaneBodyByteStableAcrossIdenticalRequests`).

**Effort levels, budgets and the operator override**

- **An unknown level never reaches a provider.** A client-supplied
  `reasoning_effort` was forwarded verbatim, so `reasoning_effort:ultra` became
  `thinkingLevel:"ultra"` and any other string an invalid enum. Both level
  mappers are bounded to the known vocabularies, and a level the model does not
  advertise is mapped onto one it does. A padded value is the level it names:
  `" high"` was read as unknown, and `" none "` as a level that forced thinking
  on the Claude path.
- **A budget beyond the int range is rejected**, in the body and in a numeric
  model suffix, instead of converting to an undefined value or overflowing
  `strconv.Atoi` into `MaxInt64`. A Claude budget never goes below the API
  floor: `minimal` is 512 tokens and the Messages API rejects anything under
  1024, so the floor is applied in the writer, where the none→minimal clamp
  reaches it too.
- **The tokenrouter clamp lands on a value its enum has** — `low`, not
  `minimal` — and the test pins the value, not only the presence of the field.
  The tokenrouter writer also emitted no field where every other writer emits
  the clamped lowest effort, so the upstream default applied where the client
  asked for none.
- **The operator override is written in the client's dialect.** A
  `providerThinking` level was injected as a top-level `reasoning_effort`, and a
  Claude Messages client sends the Claude wire, which has no such field:
  Anthropic answers an unknown top-level field with a 400. It is now written as
  the Claude dialect (a disabled thinking block, an adaptive block with an
  effort, or an enabled block with a budget clamped into the model's declared
  range). It also survives a prefill turn — the conversation-shape
  normalisation used to delete the block it had just written — and it is read
  once per request rather than once per connection attempt, with the intent
  captured before the shape is normalised and the override injected last.
- **The `providerThinking` mode is validated.** Any non-empty mode was stored
  and written straight into `reasoning_effort`, so a hand-edited settings blob
  could put an invalid effort on the wire. The vocabulary is declared once
  (`internal/thinking/modes.go`), rejected on write and dropped on read, and
  the dashboard stores the mode it enforces (`HIGH` no longer echoes back while
  the request goes out as `high`). The override never puts `on`/`off` on the
  wire either: upstream's injection chain (`chatCore.js:117-127`) falls through
  to `body.reasoning_effort = mode` when the client already set `body.thinking`,
  writing a literal no provider accepts. This port keeps the chain's precedence
  — the operator fills a gap the client left, a client-set `reasoning_effort`
  outranks the injected `thinking` block — but refuses the fall-through, so
  `on`/`off` only ever reach the wire as the dialect they were meant to be.
- **The access gate sees the same model on every endpoint.** One endpoint
  checked the allowlist against the suffixed name (`gpt-5(high)`) and the other
  two against the plain one, so the same key was allowed on one route and denied
  on another. The gate derives its candidates from the suffix-free name.
- **The OpenAI filter preserves what upstream preserves and normalises what it
  normalises**: the `cache_control` markers survive for the providers registered
  with upstream's `preserveCacheControl` quirk (the alicode family on
  DashScope — dropping them makes those gateways answer 400), and the tools are
  normalised even without a `messages` key, which used to skip the tool and
  tool_choice passes entirely. It no longer runs over a Claude Messages body: a
  provider whose own endpoint speaks Messages (`minimax-code` on the MiniMax
  mavis gateway) had its converted body  normalised back toward OpenAI, deleting
  every `tool_use` block and rewriting the tools into a shape the Messages API
  rejects. The filter is split into focused helpers, and the model-suffix scan
  uses the `bytes` package.
- **The outbound pass now keeps its hands off a body that never left Claude
  format.** The lane's `claudeNative` flag was derived from the *connection's*
  base URL (`isAnthropic` requires `https://api.anthropic.com`), while
  `chat.go` decides the other way round: provider `claude`/`anthropic` keeps the
  Claude wire whatever base URL the connection carries. A `/v1/messages` client
  on a relayed `claude` connection therefore arrived at the outbound pass with
  `target:"openai"`, and the filter rewrote its `input_schema` tools into
  `{type:"function",function:{…}}` on the way to a Messages API — the same
  symptom as the minimax-code case above, reached from the client side instead
  of the provider side. The fallback now mirrors chat.go's rule, so the pass
  and the token savers' system-prompt placement both see the format the body is
  actually in. Caught by the negative assertion in
  `TestHandleMessages_RequireClaudeToolType`, which was green before this branch
  and red with it.
- **A request whose every turn carried nothing is answered locally** instead of
  being forwarded with an empty `messages` array, which OpenAI and Anthropic
  both answer with a 400.

**Settings, database and the request path**

- A settings read that fails is an error, not a row of defaults — a transient
  failure silently turned the operator's settings off for its duration — and a
  settings write never replaces a row it could not read, which used to drop
  every other setting, the dashboard password among them. Writes are serialised
  (the row is one JSON blob, and two interleaved writers lost one another's
  keys), the cached row is copied completely (the nested header map of a
  provider override was shared with the cache), a quota written as a string is
  refused at the boundary (a wrong-typed `defaultRpm` read back as zero, and
  zero is "no limit", so the quota was lifted while the limiter stayed on), and
  the settings parser is split per section instead of one function with sixty
  conditionals.
- The capacity-adapter model list resolves renames in the provider layer — the
  retired `oc/mimo-v2.5-free` mapping lives with the model names, not inside the
  generic settings parser. The mimo bootstrap honours the request context, and
  an OAuth token endpoint's error truncates the body the way its sibling does.
- The thinking-key scan runs before the body is decoded, and the Anthropic beta
  merge asks it first instead of decoding the whole request to look for one
  field.

**Cline client identity**

Chat, OAuth refresh/exchange and OAuth model probes present the 9Router
identity (`User-Agent: 9Router/<version>`, Go runtime platform fields,
`X-CLIENT-TYPE: 9router`) instead of the `Cline/3.0.61` / `cline-cli`
impersonation the registry carried; API-key probes send `Accept`+`Authorization`
only. `HTTP-Referer` and `X-Title` stay, because upstream's `buildClineHeaders`
sends them too — they attribute the product, they do not identify the client.
The platform fields are the Go runtime's own values, which is what upstream
sends (`open-sse/shared/clineAuth.js:24-35`), with Node's platform spelling
(`win32`, not `windows`) and the `v1.27.0` version shape. Verified live on
2026-10-21 against `api.cline.bot` with the stored OAuth credential: `GET
/api/v1/models` answers 200 with 468 models and `POST /api/v1/chat/completions`
answers 200 with a real reply, identically for the new identity, the old
impersonation, and no identity headers at all — the endpoint does not validate
them.

**Cost, measured** (Intel Core i5-3427U @ 1.80 GHz, 4 cores, Linux;
`go test -benchmem`; `BenchmarkGetSettings` in `internal/db`,
`BenchmarkFilterToOpenAIFormatJSON` in `internal/translator` and
`BenchmarkBodyMentionsThinking` in `internal/thinking`):

- the settings read the fast path needs (`providerThinking`) was 94.5 µs, 53
  allocations and 5.6 KB per call — a point query plus a JSON parse, paid twice
  per request. The parsed row is now reused while the row content is unchanged
  (the row itself is still read on every call, so the answer is never stale),
  which takes the read to 47.7 µs and 18 allocations and the per-request cost
  from about 189 µs to about 48 µs.
- the thinking-key scan copied the whole body (`string(raw)`) before scanning
  it: 1.86 ms and 524 KB per call on a 440 KB body. It scans the bytes now:
  1.47 ms, 0 allocations.
- the OpenAI filter decoded and re-serialised every request body. A body it does
  not change is returned byte for byte: 4 KB goes from 16.8 µs and 46
  allocations to 9.4 µs and 35, and 400 KB from 188 µs to 144 µs. The bytes the
  client sent are the bytes the upstream sees, which is also what the upstream
  has cached before. A body the pass does rewrite is re-serialised canonically,
  which is where the json/v2 float64 limit applies — the same limit upstream's
  `JSON.parse` has; a body the pass leaves alone keeps an integer above 2^53.
- every float64 budget that exceeds the int range expresses no intent rather
  than an undefined value.

**Divergences from upstream, recorded**

- upstream `thinkingUnified.js:62-63` reads `reasoning.effort` only; this repo
  also honours a strict-bool `reasoning.enabled` (false→disable, true→auto), so
  empty-effort + `enabled:false` disables reasoning here where upstream
  expresses no intent.
- upstream `capabilities.js` does not mark the muse-spark contributor family
  reasoning-mandatory; this repo declares `ThinkingCanDisable: false` for it.
- upstream's `providerThinking` override accepts any non-`auto` mode string and
  writes it straight to `reasoning_effort` (`chatCore.js:117-131`), with no
  vocabulary at all; this port's closed vocabulary rejects an unknown mode on
  write and drops it on read, and canonicalises the case so `HIGH` and `high`
  are one mode.
- upstream's injection chain falls through to `reasoning_effort = mode` when the
  client already set `thinking`; this port refuses that fall-through (see above).
- the dialect-from-the-wire rule upstream pins for `muse` by name is applied to
  every provider on a Responses endpoint.
- `max_tokens` on the CommandCode path defaults to 1024 where upstream uses
  64000: the test ping budget depends on it.
- the CommandCode path keeps `developer`-role handling as a deliberate local
  divergence, pinned by a test.
- the Gemini capability lookup is provider-agnostic by design, and the OpenAI
  filter's assistant-with-`tool_calls` shortcut matches upstream — upstream
  returns such a message as-is — pinned by a test naming the upstream reference.

**Verification**

`go build ./...`, `go vet ./internal/...`, `go test ./internal/...` and
`go test -tags=integration ./internal/integration/...` pass on the rebased head,
and the branch carries the current upstream `main` (rebased, so the PR diff is
this branch's own work). Every determinism seam above was checked by removing
`json.Deterministic(true)` at that seam and confirming the named test fails.
