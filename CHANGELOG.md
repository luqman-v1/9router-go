# Changelog

## [Unreleased]

### ✨ `/v1/models` can report the models you can actually call (#28)

- On a fresh install `/v1/models` listed **1302 models across 119 providers** while the dashboard picker showed ~8, and nothing in the response said which list was authoritative. The two numbers came from different code paths: `buildModelsList` deliberately dumps the whole static registry when `providerConnections` is empty ("so a fresh install still has a usable picker"), while `resolveModelPickerGroups` in `web/src/components/combos/pickerData.ts` gates on `isConnected || noAuth`. A new user reads the 1302-entry list as "models I can call" and looks for a missing import step.
- The listing is now scopeable by query parameter, leaving the default byte-for-byte upstream-compatible:
  - `GET /v1/models` (default) — unchanged: full catalog on a fresh install, connection-scoped once connections exist
  - `GET /v1/models?connected=1` — only providers with an active connection, plus registry `noAuth` providers. On a fresh install this is the ~8-model noAuth subset instead of the full catalog
  - `GET /v1/models?all=1` — always the full static catalog, connections ignored, for explicit discovery
- Every response now carries `mode` (`all` | `connected` | `catalog`) and `connections` (distinct providers with an active row), so a client can tell a candidate catalog from a usable model list without guessing.
- The `disabledModels` KV scope keeps filtering in all three modes, and a custom model for an unconnected provider stays hidden under `?connected=1`.
- Adds `providers.IsNoAuthProvider`, which resolves an alias to its canonical id before reading the registry flag, and `ModelsListMode` + `ModelsListResult` on the chat handler so the mode and its metadata travel together.

### 🐛 Claude decloak now recovers a tool name the map lost (`b65d2d0a`, #4342)

- A cloaked tool name reached the client as an unresolvable `<tool>_ide` whenever `toolNameMap` missed it — the map is built per request, so a retry or a reconnect loses it. Both decloak paths now fall back to stripping the literal suffix: the non-streaming `DecloakClaudeResponseBody` and the streaming `ClaudeStreamDecloaker`. Decoy names are exempt, so they still reach the client unresolved and surface as "tool unavailable" rather than a silent no-op.
- Two places bailed out before the fallback could help. `DecloakClaudeResponseBody` returned early on an empty map, and `NewClaudeStreamDecloaker` returned nil for one. The constructor now distinguishes the two cases: a **nil** map means no cloaking was applied at all, so stripping would be wrong; a non-nil **empty** map means the map was lost, and the decloaker is built anyway so the fallback runs.
- `claude-opus-5-5` joined the `cc` and `claude` catalogues, in the registry position upstream lists it.

## [v1.9.4] - 2026-09-27

### 🐛 Issue #24 — proxy UI, account rotation, priority reorder, provider search

- 🔴 **The Apply Proxy modal was unusable past a handful of pools.** It was hand-rolled without the shared `Modal.svelte` scroll wrapper, so the flex-centred panel grew past the viewport and clipped the title and every action above the fold — with a couple of hundred batch-imported pools the user saw only an endless `Imported <ip>` list and could reach neither "One-to-one (rotate)" nor Cancel. The panel is now `max-h-[85vh]` with the pool list scrolling on its own, and a filter box narrows pools by name or URL. Bulk apply also reports `Applying N / M` and its Stop button now actually aborts the loop instead of just hiding the modal while requests keep firing.
- 🔴 **Reordering two accounts could permanently break that pair.** The dashboard swapped priorities with two independent full-row `PUT`s, which have no cross-row transaction: one failing while the other landed left two rows sharing a priority, and a stable sort over tied priorities then made every later click a literal no-op. A single `POST /api/connections/{id}/reorder` now performs the swap *and* renumbers the pool to a contiguous 1..N inside one SQLite transaction, mirroring upstream's `reorderInTx` (`connectionsRepo.js:111-133`) — partial failure is impossible and any pre-existing tie is repaired. The client gained an in-flight guard so a second click cannot fire a swap computed from a stale list, and a failed reorder now alerts instead of only reaching `console.error`.
- 🔴 **A NULL priority was silently rewritten to 0 by any unrelated edit.** `HandleUpdateConnection` defaulted the column to `0` and only restored the old value when the row already had one, so a rename, proxy assignment or model assignment promoted a legacy row to rank 0 — ahead of every other account, since 0 beats every positive rank. The parameter is now `*int` and NULL stays NULL. The same coercion existed in four background writers; they use the new data-only `Repo.UpdateConnectionData`, which also stops a background OAuth refresh from writing back the name/isActive/priority it read before the write and reverting a reorder or a toggle the user had just performed.
- 🔴 **A client-pinned connection bypassed the enable/disable toggle.** The pinned branch of `getBestConnection` only checked provider ownership, so `x-connection-id` (and the custom-node prefix pin in `resolution.go`) served a disabled, excluded or model-locked account. Upstream resolves the pin *inside* `availableConnections` (`src/sse/services/auth.js:100-148`), where those rows never match; the pin now falls through to the configured strategy on the same conditions, and cross-provider pinning is still rejected.
- **Provider search only matched display names.** Searching a provider's own id found nothing — "bai" returned "Baidu Qianfan" but never "B.AI", and "atria-asi" or "tokenharbor" returned zero results — so a user concluded the provider was missing and built a custom OpenAI-compatible node instead. `matchesSearch` now also matches the id and alias.
- **`qoder-cn` was half-registered.** It appeared in the dashboard catalog, aliases and offline models, but had no `KnownProviders` transport and no executor, so `qdcn/<model>` resolved a provider the request path could not serve. It is now registered end to end: its own gateway base URL, COSY executor, OAuth refresh config, device-flow login/poll hosts, PAT validation endpoints and quota URL — each keeping its own hosts, never aliased onto `qoder` (AGENTS.md §3.A). Note that `bai` and `atria` were already present in the working tree from `773b5d96` but sit under `[Unreleased]`; a release containing that commit is what actually reaches users.
- 🔴 **The edit modal kept manufacturing priority ties.** The priority input was seeded with `conn.priority ?? 1` and always sent back, so renaming a NULL-priority row silently assigned it rank 1 — tying it with whichever row already held that rank and recreating exactly the un-reorderable pair the new endpoint exists to repair. The field is now compared against its seeded value and the key is omitted when untouched, so only a deliberate edit changes a rank.
- **The first priority-chevron click after opening a row proxy dropdown was always dead.** The dropdown paints a `fixed inset-0 z-40` click-away backdrop over the whole viewport, and a positioned element paints above the unpositioned chevrons, so the click only ever dismissed the dropdown. The chevron group now sits at `z-[60]`, above that backdrop, and the list is raised to `z-50` to match.
- 🔴 **Round-robin state was an in-memory index, so it reset to the top account on every restart** — and the reported "round robin never reaches the next account" is exactly what that produces. Rotation is now persisted per row, ported from upstream's stateless LRU (`src/sse/services/auth.js:151-189`): `lastUsedAt` / `consecutiveUseCount` are read with the candidate set, the least-recently-used row wins once its sticky window expires, and the winner's stamp is written back. The selector lives in `selectByRecency`, replacing `rotateConnectionsSticky`; the now-dead `ResetConnectionState` and the `conn:`-prefixed in-memory keys are gone, since there is no in-memory connection state left to reset.
- **Rotation stamps need nanosecond precision, and this is load-bearing.** The least-recently-used tie-break keeps the first row in priority order when two stamps are equal, so with `time.RFC3339` (whole seconds) every account ends up carrying the same stamp after one full cycle and the rotation then locks onto the top account forever. `db.RotationTimestampFormat` is a fixed-width nanosecond RFC3339 — fixed width, not `time.RFC3339Nano`, which trims trailing zeros and would sort `…:00.5Z` after `…:00.500000001Z`. `TestApplyConnectionStrategy_KeepsRotatingPastFirstCycle` fails on pick 5 if the format is reverted to seconds.
- **Test fixtures were building a narrower schema than production.** `dbtest.CreateTables` and five hand-rolled test schemas declared `providerConnections` without the Go-only additive columns, so any query reading `lastUsedAt` failed in tests while working in a real database. `db.EnsureAdditiveColumns` is now exported and applied by those fixtures, which keeps the two shapes from drifting again the next time a column is added.
- **Providers added to the catalog without a shipped PNG rendered as an empty tile.** `getIconPath` resolves `/providers/<id>.png` for every card, and the sites hid the failed `<img>` — B.AI, Qoder CN, Dahl, Atria, Agnes, TokenHarbor (and `zai-search`) all showed a blank 32px box. The real logos were copied from upstream `decolua/9router` (`public/providers/{bai,atria,dahl,agnes,tokenharbor,qoder-cn}.png`, verified as valid PNGs), so the cards now render the genuine artwork. As a backstop, the new shared `ProviderIcon.svelte` (card tile, detail header, top bar) still falls back to an initials badge in the provider's own catalog colour on any missing asset, so a future provider that forgets to ship artwork reads as intentional rather than broken. `zai-search` is an internal tool provider with no card and no upstream logo; only the fallback covers it if it ever renders.
- Verified with `go test ./...`, a full SPA build, and a live run of the built binary driven through the dashboard UI: the reorder endpoint repaired a deliberately tied pool; a chevron click moved a row the old two-PUT path had wedged; renaming a NULL-priority row left the column NULL while an explicit priority change still took effect; and a chevron click landed while a row proxy dropdown was open.
- Round-robin was then exercised end to end against a stand-in upstream that echoes back the credential it received, over a custom OpenAI-compatible node with three accounts: six requests rotated `1→2→3→1→2→3`; the gateway was restarted mid-cycle and the next three continued `3→1→2` rather than restarting at the top account; disabling one account removed it from service (`3→1→3→1`) and an explicit `x-connection-id` pin naming that disabled account was ignored in favour of an active one.

### ✨ Gemini Live realtime STT transport (`fe347e4e`)

- 🔴 **Realtime transcription was not available at all.** The REST `generateContent` path can only transcribe a whole file inline; the Live API's `:bidiGenerateContent` WebSocket is the streaming counterpart. `internal/handlers/media/gemini_live_stt.go` ports it: audio goes up as `realtimeInput.mediaChunks` in 16 KiB slices (~0.5s of 16-bit 16kHz mono PCM) and the server's incremental `serverContent.inputTranscription` deltas accumulate into the transcript.
- **Dispatch is on a transport marker, never a model id.** `providers.ResolveModelTransport` reads the `transport` field off a catalog entry (`modelTransports`, mirroring the registry field upstream reads in `sttCore.resolveModelTransport`), so a new realtime provider extends through data rather than a new hardcoded branch. `gemini-2.5-flash-native-audio-preview-09-17` joined the gemini catalogue with `kind: "stt"` and `transport: "gemini-live"`, and a caller-supplied `transport` still wins — same precedence as upstream.
- Audio is only sent after the server's `setupComplete`, and the run settles on `turnComplete` — or on a graceful close, where a partial transcript beats a hard error and silence is the hard error. `goAway` advisories rotate the socket once per call: setup is replayed and audio resumes from the byte offset already sent, so nothing is re-uploaded and the transcript survives the hop.
- Lifecycle knobs (`prompt`, `language`, `system_instruction`, `setup_timeout_ms`, `turn_timeout_ms`) ride the same form pass-through every transport already gets, so no engine change was needed to reach this leaf. `system_instruction` replaces the built-in directive wholesale; `prompt`/`language` only shape the default. Caller-supplied timeouts are clamped at 300s.
- `verbose_json` answers with `segments` built from the raw deltas. Those frames carry **no timestamps**, so segments expose `{id, text}` only — start/end/duration are deliberately absent rather than fabricated as zeros, which would misrepresent provider data to anyone diffing transports.
- New dependency: `github.com/gorilla/websocket` (Node has a global `WebSocket`, which is why upstream needed none).
- Tested end to end against a stand-in Live server: `toLiveWSURL` shapes, the setup frame's model/generationConfig/systemInstruction, every audio byte arriving as base64 (20 000 bytes round-tripped), the flushing turn, transcript assembly from two deltas, partial-transcript-on-close versus silence-as-error, the system-instruction precedence, and the timeout clamp.

### ✨ Claude header parity — session id, beta merge, rate-limit forwarding

- **`x-claude-code-session-id` on OAuth requests** (`6aea3875`). The cloak generates a `metadata.user_id` carrying a session id, but nothing echoed it back in the matching header, so Anthropic saw two different sessions for one request. `extractClaudeSessionIdFromUserId` reads the id back out (JSON object form, which is what the cloak writes, or a bare id from other clients) and the request-scoped header is set from it. `generateFakeUserID` now also strips the CLI's own `claude:` prefix before baking the id into the body, so the two agree without a second pass.
- **The caller's `anthropic-beta` flags are merged, not dropped** (`dc198dff`). A client asking for a beta the gateway does not list was silently refused without being told why. `handlerutil.WithClientAnthropicBeta` carries the header on the context alongside the session id, and `providers.MergeAnthropicBeta` unions it with the registry's own list, de-duplicated and in order.
- **Retry and rate-limit headers are forwarded to the client** (`dc198dff`). `retry-after`, `x-should-retry` and every `anthropic-ratelimit-*` header are copied from the upstream response in `ForwardOpenAI`, before any branch writes a status — so they reach the client on the streaming, non-streaming and Claude-translated paths alike. Without them a 429 arrives as an opaque failure and a gateway in front of us cannot back off on our behalf. Hop-by-hop, auth and content headers stay internal.
- Header edits are request-scoped: `providers.WithHeader` and `WithoutBetaFlag` both copy, because the map in `KnownProviders` is shared by every request and a mutation would leak into later ones. `TestWithHeader_DoesNotMutateSharedMap` guards it.
- Not ported here: **Claude free-tier reset grants** (`consumeClaudeResetGrant`, `?cedar_ember=1` plus a new `claude-reset` API route). It is a new usage-side flow, not a header one — see the outstanding list.
- `TestExtractClaudeSessionIdFromUserId` pins a quirk on purpose: the prefix is matched at the start only, and leading whitespace is trimmed *after* the match, so `"  Claude: abc"` keeps its prefix — exactly what upstream's anchored replace does.

### ✨ Port the full upstream pricing table (271 rate entries) and the real cost formula

- 🔴 **Costs in the Usage views were a guess.** The Go port had four hand-written prefix rows (`claude-sonnet-4`, `claude-haiku`, `deepseek-v4-flash`, `gpt-4o`) and charged everything else a fabricated **$1/M input, $3/M output** — and charged cache reads at the *full* input rate, because the call site never passed the cached or cache-creation counts. `internal/pricing/tables.go` is now generated from `open-sse/providers/pricing.js`: 108 canonical models, 112 provider-specific entries, 51 pattern rows, five rates each (input, output, cached, reasoning, cache_creation). Regenerate rather than hand-edit.
- `GetPricingForModel(provider, model)` replaces `lookupPricing(model)` and follows upstream's four steps: the provider's own table, the free namespace, the canonical table (vendor prefix stripped, then whole id), then the ordered pattern table. `path.Match` is unusable here for the same reason it is in the thinking-levels port — its `*` never crosses `/` — so the glob keeps upstream's semantics.
- `CalculateCost` is the port of `calculateCostFromTokens`. The convention it assumes is the one the wire uses: **`prompt_tokens` is cache-inclusive**, so cached and cache-creation are subtracted before the input rate and then charged at their own cheaper rate; a rate left at zero falls back the way upstream does (cached → input, reasoning → output, cache_creation → input).
- The call site in `handlers/chat/usage.go` now passes the provider and the full token mix, so a cache-heavy request stops being billed as fresh input.
- ⚠️ **Visible change in the Usage totals: 556 of the 1592 catalogued `(provider, model)` pairs are priced by nobody, upstream either.** Upstream records a cost of **0** for those; the Go port used to invent $1/$3 per 1M for them. This follows upstream and makes "unpriced" visible instead of quietly fictional, but it does mean the cost column drops for those models from here on — say the word if you would rather have a fallback rate back.
- Tests pin the **numbers upstream itself returns**, not a re-implementation: `TestGetPricingForModel_Parity` and `TestCalculateCost_Parity` use rates and costs read out of the upstream module (anthropic sonnet $18.8625, gpt-4o $13.6875, gpt-5.6-sol $36.875, vendor-prefixed deepseek $0.4137, free namespace $0, unpriced $0 — all on prompt 1M / completion 1M / cached 250k / reasoning 100k / cache-creation 50k). The old `cost_test.go` asserted the four-row table and the prefix-matching behaviour, neither of which exists upstream, so it was replaced.
- `TestPricingTablesPopulated` guards the generated file: an empty table would still compile and would silently price everything at zero.
### 🐛 Codex "Add Connection" died at `invalid_authorize_request` (upstream loopback parity)

- **Symptom**: `Providers → OpenAI Codex → Add Connection` opened the OpenAI login page only to bounce straight to `Authentication Error` (`error_code: unknown_error`), and the modal never connected. Older builds surfaced the same rejection as `{"code":"invalid_authorize_request","message":"Invalid authorize request"}`.
- **Root cause**: `callbackRedirectURI` (`internal/handlers/oauth/redirect.go`) derived the callback from the dashboard's own host for *every* provider, so Codex was sent `redirect_uri=http://localhost:<dashboardPort>/callback`. Codex's public OAuth client (`app_EMoamEEZ73f0CkXaXp7hrann`, the Codex CLI) has exactly one registered loopback URI, and `auth.openai.com` validates `redirect_uri` during the authorize step. Probed live in a browser against `auth.openai.com`: `http://localhost:1455/auth/callback` reaches the login page, while `http://localhost:20130/callback`, `http://localhost:20130/auth/callback` and even `http://localhost:1455/callback` (right port, wrong path) are all rejected before the login form renders.
- **Fix, in three parts**:
  - `pkceConfig` gained `fixedRedirectURI` / `fixedPort`, and `callbackRedirectURIFor` / `exchangeRedirectURIFor` make the registered URI win over the dashboard-derived one for providers that pin it. Every other PKCE provider keeps following the dashboard, unchanged.
  - `internal/handlers/oauth/codex_proxy.go` (new) ports upstream `startCodexProxy` (`src/lib/oauth/utils/server.js`): a short-lived listener on `127.0.0.1:1455` behind `GET /api/oauth/codex/start-proxy`, a state-keyed session carrying the PKCE verifier, and `GET /api/oauth/codex/poll-status` so the dashboard learns the outcome. The listener completes the token exchange **server-side** and renders the result in the popup; an unregistered callback (a real `codex` CLI on the same machine) is 302'd to the dashboard's `/callback` instead of being dropped.
  - The exchange itself moved out of the HTTP handler into `resolvePKCEExchange` → `requestToken` → `buildConnectionData` → `persistConnection`, so the loopback server and `POST /api/oauth/pkce/exchange` run one code path instead of two copies of the OAuth rules.
- **A second, quieter Codex gap closed at the same time.** Upstream stores `chatgpt_account_id` / `chatgpt_plan_type` from the id_token into `providerSpecificData` and sends `chatgpt-account-id` on every call; Go stored neither and sent no such header. That is not cosmetic — the codex responses endpoint rejects a request without it, so even a successfully added account could not complete a single call. `codexAccountInfo` (`codex_account.go`) decodes the namespaced `https://api.openai.com/auth` claim block (with the legacy root-level `account_id` / `plan_type` as fallback) and `ForwardCodex` now sends the header from `ConnData`.
- **Two listener-lifetime bugs found by testing, both fixed**: `Server.Close()` from inside the request handler killed the connection serving it, truncating the very result page the user was looking at — shutdown is now `Shutdown`, deferred off the handler goroutine. And a stray callback retiring the listener would silently kill an unrelated in-flight login (the port is fixed, so a dead listener is a dead login with no way back), so the listener now only retires once nothing is pending. The dashboard also stopped freezing on "Waiting for popup authorization…" when the server no longer tracks the login; it says so and asks for a retry.
- Verified live against a worktree build on `:20141`, upstream on `:20128`: `Add Connection` now lands on `https://auth.openai.com/log-in`, `127.0.0.1:1455` is listening, `poll-status` reports `pending`, and a callback with an unusable code produces OpenAI's real `401 token_expired` in the popup and in the modal — i.e. the token request genuinely reaches `auth.openai.com`. A/B on the authorize endpoint: `:20130` (old) sends `http://localhost:20130/callback`, `:20141` (fixed) sends `http://localhost:1455/auth/callback`. `go test ./...` and `bun run build` green; `TestCodexLoopback_completesLoginEndToEnd` drives the real listener through a full login against a stub token endpoint.

### ✨ Sync batch — Codex `responsesLite` and the connection name guard

- **`responsesLite` for `gpt-6-sol` / `gpt-6-luna`** (`95600db1`). Those two models were in the catalogue but would 400: Codex 0.155 serves them with a different request shape — tools and instructions travel as an `input` prefix (`additional_tools` + a developer message) instead of top-level fields, and reasoning carries `context: "all_turns"` with no `summary`. Adding the ids to the catalogue without the wire shape was worse than leaving them out, so `internal/proxy/executor/codex_responses_lite.go` ports it: `isCodexResponsesLiteModel` (stripping a trailing `(level)` override first), the prefix rewrite, and the lite reasoning block (effort defaulting to medium rather than low). A body that already carries the prefix is left untouched, so replaying a transcript is not double-wrapped, and an input shape the rewrite cannot read is left alone rather than replacing the conversation with the prefix — the two build paths produce different concrete slice types, `[]any` and `[]map[string]any`. Classic Codex models keep the shape they had; `TestBuildResponsesBody_ClassicCodexUnchanged` pins that.
- Not ported: upstream falls back to `CODEX_DEFAULT_INSTRUCTIONS` when the client sends none, and 9router-go has no copy of that prompt blob for any model — the prefix carries the caller's own instructions, and adds no developer message when there are none. Carrying a Codex prompt only on the lite path would be a new, unreviewed prompt rather than a parity fix.
- **`POST /api/connections` refused to overwrite nothing** (`239bcfc5`, fixes #4311). A create reusing a name already in use replaced the stored key without a word: a script naming rows "Key 1", "Key 2", … kept wiping existing pool entries and got a success back. A create now looks the name up first and answers **409 `PROVIDER_NAME_CONFLICT`** naming the row that would have been replaced; a caller that means it passes `allowOverwrite: true` (or the legacy `overwrite`), which rewrites that row in place, keeping its id and its place in the rotation instead of leaving two rows the account picker cannot tell apart. A request carrying an `id` is already an explicit edit and is never name-guarded. Go's symptom was a silent duplicate rather than a silent replace, because `CreateProviderConnectionFull` is a plain INSERT — same caller-facing hole, different shape.
- The O(1) half of that upstream commit was already ported (`MAX(priority)+1`, no renumber pass on insert).
- Live: `POST /api/connections {"provider":"commandcode","name":"9router",…}` without an id answers 409 with `existingId`/`existingName`, and the stored connection is still the only one — one row, key intact.

### ✨ Sync batch — 5 aggregator providers, catalogue and Cline free tier

- **Five OpenAI-compatible aggregators** added upstream in v0.5.91, wired end to end: `tokenharbor` (+`th`/`thh`), `dahl` (+`dahl-inference`), `atria` (+`atria-asi`), `agnes` (+`agnes-ai`), `bai` (+`b-ai`). A provider entry is only real once every table carries it, so this touches `providers.go` (transport), `aliases.go` (short prefixes), `registry_models.go` (offline catalogue), `executor/init.go` (routing), `web/src/lib/providers.ts` (dashboard card, notice, `modelsFetcher`) and `web/src/lib/models.ts`. Upstream ships no capabilities or pricing rows for any of the five, so none were invented here.
- The dashboard "Suggested free models" import needs a live catalogue endpoint, which upstream wires per provider (`PROVIDER_MODELS_CONFIG`). `providers.ModelsListURL` carries that mapping and `HandleGetConnectionModels` gained a branch that Bearer-probes it. `agnes` and `bai` publish no seed catalogue at all — their page correctly lists no models and imports everything live.
- **`passthroughModels` needs no Go counterpart.** Upstream added a per-provider opt-in so any model id is accepted; the Go request path already forwards the model string untouched for every provider, so it is a superset. Deliberately not "implemented" — adding a gate would be a regression.
- **Catalogue sync**: `opencode-go` 28 → **42** models, `codex` 27 → **29** (`gpt-6-sol`, `gpt-6-luna`), both now byte-identical to the upstream registry. `qoder-cn` was missing from the dashboard catalog entirely and is back.
- **Cline free tier** (`199bcfc5`). `/api/v1/models` carries no `cline-free/*` ids, so upstream merges a second, unauthenticated feed (`/api/v1/ai/cline/recommended-models`) and prices that namespace at zero. Both are here: `clineFreeTierModels` merges with first-writer-wins so a dead feed cannot take the catalogue down, and `pricing.IsFreeModel` checks the namespace before the price table — the same model id is *not* free through a paid gateway.
- Parity fixture re-captured for the wider catalogue: **1589/1589** `(provider, model)` pairs match upstream (was 1547). The 42 new pairs include `gpt-6-sol`/`gpt-6-luna`, which are the first to exercise the per-model `thinkingLevels` stage.
- Live, `20130` vs upstream `20128` on the same data: all six provider pages identical — `tokenharbor` 6 rows / 7 level options, `dahl` 3 rows (incl. the binary `thinking` level), `atria` 1 row, `agnes` and `bai` 0 rows with no picker, `qoder-cn` 16 rows.

### ✨ Sync batch — upstream v0.5.86..v0.5.91 (Gemini turns, Claude thinking text, usage by API key)

- **Gemini terminal-turn guard** (`30464bc2`). `NormalizeGeminiContents` merged same-role turns and stripped empty parts, then stopped — a conversation that ends on a `model` turn is rejected by Gemini, and three ways arrive that way: a prefill, a truncated stream, and tool calls the client never answered. It now brackets the payload with user turns: a leading `user "..."` when the first turn is not a user turn (that part was already missing, from #e7b5f09), and a trailing turn carrying one `functionResponse` per unanswered `functionCall` — or a plain `Continue.` when the model was talking. `GeminiFunctionCall`/`GeminiFunctionResp` gained `ID`; without it a synthesized response could not be matched to its call. Ported upstream's own `gemini-contents-normalization.test.js` as a table test.
- **Claude thinking text for OpenAI clients** (`90b06934`). Claude returns thinking as a signature only unless the request sets `thinking.display: "summarized"` — a field OpenAI has no equivalent for, so a client asking with `reasoning_effort` (Chat Completions) or `reasoning.summary` (Responses) silently got nothing: `ensureMessagesMaxTokens` deleted `reasoning_effort` outright. The intent is now captured into the Claude field before the OpenAI-only keys are stripped (`internal/proxy/executor/claude_thinking.go`), and `redact-thinking-2026-02-12` is dropped from `Anthropic-Beta` for that request, since it asks Anthropic for exactly the signature-only behaviour the client is trying to avoid. An explicit `thinking.display` on the body still wins, and `reasoning_effort: "none"/"off"` stays an opt-out.
- **Usage by API key** (`4a57df8b`, `a406381f`). `UsageStatsResponse.ByApiKey` was declared and initialised but never written, so the dashboard's per-key breakdown was permanently empty. It is now filled from both the daily rollup and live `usageHistory`, keyed by the **stored** key value rather than a re-derived display mask — every key an instance mints shares the same `sk-{machineId}` head, so masking collapsed a whole team key set into one row and attributed one key's usage to another. Friendly names resolve against the `apiKeys` table, indexed under both the full key and its mask because the shared database holds both forms (rows this build writes are masked, rows the Next.js dashboard wrote are not).
- `handlerutil.MaskAPIKey` is now the single mask implementation, widened from a 4-character prefix to 8 to match upstream's keep-the-tail change. The `***` sentinel for short/empty keys is kept so existing no-key usage rows stay in one bucket instead of splitting.
- Three existing translator tests indexed `contents[0]` / `contents[1]` and counted turns; they now locate the part by role or by scanning, since the bracketing deliberately changes the shape.
- Live: `/api/usage/stats?period=7d` went from an empty `byApiKey` to 506 buckets with the two local keys resolving to `luqman` and `mac`. `go test ./...` and `bun test` green.

### 🐛 Thinking levels drifted from upstream v0.5.91 (MiMo + Codex per-model sets)

- Found while auditing the upstream tag range `v0.5.86..v0.5.91` (38 commits, 123 files). `internal/providers/thinking_levels.go` was missing two pattern rows and a whole resolution stage that v0.5.91 added.
- Missing rows: `*mimo*v2.6*` and `*mimo*v2.5-pro*`, both `["none","low","medium","high","xhigh"]`. Upstream's note: *mimo-v2.5-pro on opencode-go rejects reasoning_effort "max" (probed live); v2.5 accepts it*. Without them, 15 `(provider, model)` pairs fell through to the `deepseek` format default and answered `["high","max"]` where upstream answers `["low","medium","high","xhigh"]` — the picker would have offered a level those gateways reject.
- Missing stage: upstream resolves a per-model `thinkingLevels` off the catalog record before the pattern table (`getProviderModels("cx").find(e => e.id === baseId)?.thinkingLevels`, with a trailing `(level)` suffix stripped first). Go's catalog is a flat id list, so the field lives in `codexModelThinkingLevels`; `GetThinkingLevels` now resolves registry-declared → pattern → format default, the order upstream uses.
- The parity fixture had been **masking this**: it was captured from an upstream tree carrying the older values, so `TestGetThinkingLevels_MatchesUpstreamFixture` reported 1547/1547 while 15 pairs were wrong. The fixture now carries an `upstreamVersion` field, the test fails when it does not match the tag the port targets, and the capture was re-taken from v0.5.91 — 1547/1547 against the real thing.
- `TestCodexModelLevels` covers the per-model stage directly (including the `(level)` suffix and the codex/cx split), since the GPT-6 models are not in the Go catalog yet and the fixture cannot reach that path.

### ✨ Provider detail parity for `/dashboard/providers/<id>` (CommandCode audit against upstream `:20128`)

- 🔴 **The "Available Models" list was empty for 28 providers.** `web/src/lib/models.ts` keyed `PROVIDER_MODELS` by provider id but resolved it through `PROVIDER_ID_TO_ALIAS`, which holds the *display* prefix (`uiAlias`: `commandcode→cmc`, `deepseek→ds`, …). Upstream derives the same map from the registry `alias` and only remaps OAuth entries (`providerModels.js:107-113`), so every non-OAuth provider whose alias differs from its id got `undefined` instead of a list. `getModelsByProviderId` now resolves by id first and falls back to the alias.
- `internal/providers/thinking_levels.go` (new) — port of upstream `open-sse/providers/thinkingLevels.js`: the shared level sets, `FORMAT_LEVELS` keyed by thinking format, the ordered `PATTERN_THINKING` overrides (codex GPT-5.6, deepseek v4, per-model codebuddy sets), and the Kiro `resolveKiroEffortPath` gate. `path.Match` is unusable for the pattern table because its `*` never crosses `/`, so the port carries upstream's glob semantics.
- `Capabilities` gained the thinking block (`ThinkingFormat`, `ThinkingCanDisable`, `ThinkingRange`, `ThinkingEffortSupported`) plus provider-declared `ContextWindow`/`MaxOutput`. `ThinkingCanDisable` is a `*bool` because upstream's default is `true` and a Go bool cannot say "not specified" — without the tri-state, every table entry that names a format silently lost the `none` level. `mergeCapabilities` takes the whole thinking block from an overlay only once it declares a format, mirroring the JS spread.
- Ported upstream's 145 thinking declarations across the model / provider / pattern tables, and the 8 pattern rows the Go table was missing (`*gemini-3.7*`, `*grok-4.6*`, `*glm-5.3*`, `*glm-5.2*`, `*deepseek-v4.*`, `*minimax-m2.5*`, `*mimo*v2.6*`, `*muse*spark*`). Removed the table entries upstream does not have and that therefore shadowed a pattern row (`grok-4.5`, `grok-4.6`, `gpt-6-astra`, `glm-5.3`, the `gpt-5.6-*-image` trio, `*hy4*`, `*longcat*`, the `qoder` provider scope), and corrected `union-alpha` plus the `*mimo*` rows.
- `internal/handlers/dashboard/model_caps.go` (new) — `GET /api/models/caps?provider=<id>`, the Go counterpart of upstream `useModelCaps`: the model *catalog* still ships as a static bundle, but capabilities and thinking levels are resolved server-side from the provider registry, the capability tables and the synced models.dev catalog. The provider resolves by id or alias, so `/dashboard/providers/commandcode` and the `cmc/` storage prefix share one block.
- CommandCode capabilities are now the upstream short-circuit (`capabilities.js:570-582`): every model is served from one `/alpha/generate` wire, so the per-family patterns (deepseek-v4 → vision false, thinkingFormat deepseek, …) must not win. `internal/providers/capabilities_commandcode.go` ports the 23-model text-only denylist and declares `reasoning`, `thinkingFormat: "commandcode"`, `thinkingEffortSupported`, `contextWindow: 1000000`, `maxOutput: 384000`.
- Dashboard: the "Thinking:" picker is now the union of the levels this provider's models accept, with `auto` first and the picker hidden when no model has reasoning (upstream `providerThinkingLevels`); the `(level)` suffix is gated per model and shown in the model row, not only copied; the vision/reasoning icons come from the server caps instead of being empty for every built-in model.
- Model rows now render in **registry order**, the way upstream does: `models` there is `getModelsByProviderId()` verbatim (`providers/[id]/page.js:158`) and the A–Z sort this port added is gone. `buildAvailableModels` also lists the catalog first and appends custom models, instead of hoisting customs to the top. The bundled web catalog had drifted out of registry order for `commandcode`, `deepseek`, `gcli`, `openai` and `ocz`; all four are now in the upstream sequence (the Go `registry_models.go` already was).
- Live verification against upstream `:20128` on the same data. `commandcode` is byte-identical: 22 rows, the same 22 ids in the same order, the same per-model vision/reasoning icons, and the same picker (`auto, low, medium, high, xhigh, max`); the `(high)` suffix appears and clears identically. `openai` (21), `xai` (6) and `blackbox` (10) also match row-for-row including icons and order. The 16 LLM providers that rendered zero models now render theirs; the media/embedding/STT providers correctly render none on both.
- Resolved below: the `deepseek-v4-flash` vision icon was not stale data but a broken catalog sync — see "Synced models.dev catalog recorded every model as text-only".
- Tests: `TestGetThinkingLevels_MatchesUpstreamFixture` replays upstream's own resolver over all 1547 `(provider, model)` pairs in the catalog — **1547/1547 match**. Plus `TestGetCapabilitiesDetailForModel_CommandCode`, `TestIsCommandCodeTextOnly`, `TestMergeCapabilities_ThinkingIsDeclarative`, `TestGetThinkingLevels_Parity` (upstream's vitest expectations), `TestGetThinkingLevels_NoReasoning`, `TestResolveKiroEffortPath`, `TestMatchThinkingGlob`, `TestHandleGetModelCaps_*`, and `web/src/lib/models.test.ts`. `TestQoder_Capabilities` was removed: it asserted a `qoder` capability scope that upstream does not have, so it contradicted the parity it claimed.
- Still open, left alone on purpose because each one adds or removes a model rather than fixing a rendering defect: 8 providers whose bundled catalog content differs from the registry (`ag`, `cx`, `gemini`, `huggingface`, `kr`, `opencode-go`, `oc`, `xiaomi-mimo` — the `oc` and `huggingface` extras are local free models such as `space-bunny-free` / `nemotron-3-ultra-free`), and 4 registry providers with no bundled catalog at all (`qdcn`, `tokenharbor`, `dahl`, `atria`). No order-only difference remains.

### 🐛 Synced models.dev catalog recorded every model as text-only

- Symptom: capability icons that upstream shows never appeared, and the token limits in `/v1/models` came from the substring table instead of the catalog. `deepseek-v4-flash` rendered without a vision icon while the upstream dashboard rendered one.
- Root cause — the sync read a field models.dev no longer publishes. `SyncModelCatalog` decoded `modality: {image: true}` (a boolean map); the API serves `modalities: {input: ["text","image",…], output: […]}` (string arrays), so the decoded map was always empty and the writer stored `vision: false` for **every** model. Second defect: modalities were filed under the bare model id, aggregating across gateways, while upstream keys them `provider:model` precisely because gateways disagree about the same weights — some do not proxy images at all. Third: `SyncedCatalog.UnmarshalJSON` read `syncedAt` into a `[]byte`, which `encoding/json/v2` accepts only as base64, so neither an ISO string (this writer) nor epoch milliseconds (upstream's writer) loaded — a restart could not read back the file it had just written, which also dropped the `LastSync` state.
- Fix: `internal/providers/catalog_sync.go` decodes `modalities.input` with upstream's `MODALITY_BY_INPUT` mapping (image→vision, pdf, audio, video), keys modalities `provider:model` with the local alias filed too (`claude` → `anthropic`), and `GetCatalogModalities` takes the provider so a gateway's entry only answers for that gateway. A bare-model key is still accepted, so a catalog written by an earlier build keeps resolving. `syncedAt` is now written as an ISO string and decoded from ISO, epoch milliseconds, or absent.
- After the first background sync the file holds **4780** real modality entries (was 2399 all-false). `/api/models/caps?provider=deepseek` now reports `deepseek-v4-flash` as `vision: true`, and the provider detail page is row-for-row identical to upstream again — same ids, same order, same icons.
- `TestSyncModelCatalog_Modalities` (was a test that re-implemented the parser it was checking, so it passed against the broken schema — now drives the real `SyncModelCatalog` end-to-end against the current payload), `TestSyncModelCatalog_ModalitiesArePerProvider`, `TestLoadCatalogFromFile_SyncedAtForms`, `TestLoadCatalogFromFile_LegacyBareKeys`, `TestCatalogProviderKeys`, `TestCatalogBaseID`.

## [v1.9.3] - 2026-09-26

### 🐛 Kiro tool calling never reached the client (`arguments: {}` / no `tool_calls`)

- Symptom: `POST /v1/chat/completions` with `tools` never returned `tool_calls` on Kiro. The model wrote pseudo tool calls as plain text (`<invoke name="browser">…`), or — once the catalogue was sent — replied with an empty turn carrying only `reasoning_content: "..."`. Other providers (grok-cli, antigravity) were unaffected.
- Root cause 1 — **request**: Kiro has no OpenAI-style `tools` array. The tool catalogue must be attached to the last user turn as `userInputMessage.userInputMessageContext.tools` (`{toolSpecification:{name,description,inputSchema:{json:…}}}`, upstream `normalizeKiroToolSpecs`). The Go translator only sent `toolResults`, so the model never learned which tools existed.
- Root cause 2 — **response**: Kiro splits one tool call across several `toolUseEvent` frames — the first carries only `name` + `toolUseId`, the following ones carry slices of the JSON arguments under `input` (`"{\"ci"`, `"ty\": \"Jak"`, …), and a final `stop: true` frame closes it. The old code read a `content` field that is never sent and emitted `arguments: "{}"` per frame.
- Fix:
  - `internal/translator/kiro_tools.go` (new) — port of upstream `normalizeKiroToolSpecs`: OpenAI/Claude tool normalization, name sanitization (`[^a-zA-Z0-9_-]` → `_`, de-duplication suffix, 64-char cap), `inputSchema.json` forced to `type: object`, `additionalProperties` stripped, `required` filtered down to properties that actually exist.
  - `internal/translator/kiro.go` — the tool catalogue is attached to `userInputMessageContext.tools` on the last turn; `toolResults` still travel alongside it.
  - `internal/proxy/executor/stream.go` — `kiroToolCall` buffers arguments per `toolUseId` (accepting both fragmented `input` strings and objects, with the legacy `content` kept as a fallback), and `tool_calls` are only emitted once the stream ends, with the reassembled arguments.
- Live e2e verification (tool `get_weather`, "weather in Jakarta"): `kr/auto` → `{"city":"Jakarta","unit":"celsius"}`, `kr/claude-sonnet-4.5` → `{"city":"Jakarta"}`, while `gcli/grok-4.5` and `ag/gemini-3.8-flash-high` keep passing. The upstream instance (`:20128`, same Kiro account) returns the same `tool_calls`.
- Tests: `TestOpenAIToKiro_AttachesToolSpecsToLastUserTurn`, `TestOpenAIToKiro_ToolResultsStillTravel`, `TestOpenAIToKiro_NoToolsNoContext`, `TestKiroNormalizeRootSchema`, `TestKiroUniqueToolName`, `TestForwardKiroRequest_ReassemblesFragmentedToolInput`. Suite: 1473 pass.

### 🐛 Kiro `403 The bearer token included in the request is invalid`

- Symptom: Kiro chat and `POST /api/models/test` failed with 403 while the token was actually valid — `GET ListAvailableModels` with that same token returned 200.
- Root cause: `extractAPIKey` always preferred `apiKey`, and a Kiro connection created by *import* stores **two** credentials (`apiKey` + `accessToken`). That `apiKey` was sent as both `Authorization: Bearer` and `x-amz-sso-bearer`, while upstream (`open-sse/executors/kiro.js` `buildHeaders`) uses the apiKey only for `authMethod: "api_key"` connections and the `accessToken` everywhere else. CodeWhisperer rejects that token with "bearer token invalid" even though the `accessToken` is perfectly healthy.
- Fix:
  - `internal/handlers/chat/connections.go` — `resolveProviderAuthToken` applies that upstream rule in the forward path (Kiro only; every other provider is untouched).
  - `internal/proxy/grokcli.go` — `ForwardKiro` now sends `TokenType: API_KEY|EXTERNAL_IDP` according to `authMethod` plus the `x-amzn-codewhisperer-profile-arn` header, and **rotates endpoints** like upstream: `q.<region>` → `codewhisperer.<region>` → `runtime.us-east-1.kiro.dev`, falling back on 401/403/404 (`KIRO_ENDPOINT_FALLBACK_STATUSES`); 400 stays terminal. AWS hosts are regionalized from `providerSpecificData.region`.
- Verification: `POST /v1/chat/completions {"model":"kr/claude-sonnet-4.5"}` → **200** (`"ok"`), previously 403; all three Kiro surfaces also answer 200 when probed directly with the same `accessToken`.
- Tests: `TestResolveProviderAuthToken_Kiro` (7 cases: imported/builder-id/api_key/access-only/key-only/empty/other provider), `TestKiroEndpointsOrdering` (3), `TestKiroTokenType` (5), `TestKiroEndpointFallbackStatus` (401/403/404 rotate, 400/429/5xx terminal). Suite: 1466 pass.

### ✨ `GET /v1/models` — live catalog and response shape identical to upstream

- `internal/handlers/chat/live_catalog.go` (new): port of upstream's `LIVE_MODEL_RESOLVERS` — **kiro** (`GET https://q.<region>.amazonaws.com/ListAvailableModels` with a Kiro IDE fingerprint UA, each model expanded into `-thinking`/`-agentic`/`-thinking-agentic` variants, `auto` without agentic variants), **grok-cli** (`GET <base>/v1/models` with the `x-grok-cli` headers), and **custom nodes** (`fetchCompatibleModelIds`: `GET <baseUrl>/models`). In-process cache of 5 minutes per credential; 401/403 triggers a single token refresh and a retry; failures fall back to the static catalog and never empty a provider.
- A resolver is only consulted when `enabledModels` is not pinned on the connection, exactly like upstream; the live catalog replaces the static one while custom models and aliases are still merged in.
- Response shape aligned with upstream: kiro `capabilities` uses the live `{thinking, agentic}` block verbatim; combos use a separate `ComboCapabilities` shape (no `thinkingEffortSupported`, `tools` = AND across leaves, `contextWindow` = narrowest leaf, `maxOutput` = widest leaf — the `aggregateComboCapabilities` rules); `context_length`/`max_completion_tokens` are gone from combo entries (`omitzero` is required because `encoding/json/v2` does not drop zero numbers under `omitempty`); the `outputAlias/` qualifier is stripped only from registry/alias ids, never from custom ids (which is why `openrouter/openrouter/free` stays double-prefixed upstream too).
- `internal/providers/catalog_sync.go`: the models.dev catalog file written by upstream is now readable (its `v2`/`etag`/epoch-millisecond `syncedAt` format), and `GetCatalogLimits` is consulted before the substring guesses, so token limits follow the catalog instead of heuristics.
- Cross-check against the running upstream instance (`:20128`, same database): **714 models upstream vs 713 in 9router-go, all 713 ids identical, 0 entry-key and 0 `capabilities`-key differences**. Breakdown: `clinepass` 469/469, `kr` 34/34, `ag` 20/20, `cbai` 15/15, `openrouter` 7/7, `nvidia` 4/4, `Id` 3/3, `gcli` 1/1, `tr` 153/154, combos 7/7.
- Remaining: (a) `tr/typesafe/jev-1.13` exists only upstream — the tokenrouter node's `GET <baseUrl>/models` now returns nothing, so it is a leftover upstream cache entry; (b) the **values** of `context_length`/`max_completion_tokens` still differ for 627 ids because upstream's `MODEL_CAPABILITIES`/`PATTERN_CAPABILITIES` tables are not ported yet (the models.dev catalog only covers 23 providers) — the model list and the response shape are identical, the numbers are not.
- Tests: `live_catalog_test.go` (kiro variant expansion, `kiroDisplayName`, grok-cli catalogue parser with 5 cases, region from profileArn) + `live_catalog_handler_test.go` (live kiro replaces the static catalog, `enabledModels` skips the live fetch, failures fall back to the static catalog, live grok-cli and its headers).

### 🐛 `GET /v1/models` — full port of upstream's Next.js `buildModelsList` (fixes issue #1)

- Symptom: `/v1/models` flooded the catalogue — `ghost customs` in `kv.customModels` for unconnected providers were published, alias keys were published as models of their own, `enabledModels` was ignored, and media/embedding models leaked in. On the user's real database: 640 models, `clinepass/` duplicated with `cp/`, and 4 dead nodes still exposing 21 models.
- Source of truth: `~/htdocs/9router/src/app/api/v1/models/route.js` (`buildModelsList`, `KIND_SLUG_MAP`, `MODEL_TYPE_TO_KIND`, `inferKindFromUnknownModelId`, `aggregateComboCapabilities`).
- `internal/handlers/chat/models_list.go` (new; the builder moved out of `chat.go` — 1331 → 897 lines):
  - Upstream ordering: **combos first** (`owned_by: "combo"`, capabilities aggregated across every leaf), then per-connection models.
  - `isActive !== false` **only** — upstream requires no credentials for listing, so 9router-go's credential gate was deliberately reverted (`TestHandleModels_SkipsCredentiallessConnections` was replaced by `TestHandleModels_ActiveConnectionPublishesCatalog`).
  - Per connection: `staticAlias` = catalogue alias, `outputAlias` = `providerSpecificData.prefix`; `enabledModels` (psd or top-level) **replaces** the static catalogue; then custom models whose `providerAlias ∈ {staticAlias, outputAlias, providerId}` (llm-typed only) and `modelAliases` targets with a matching prefix are merged in; `isDisabled(outputAlias|staticAlias, id)` is enforced; `outputAlias/`, `staticAlias/` and `providerId/` qualifiers are stripped from ids.
  - Alias keys are **no longer** model ids themselves (upstream only uses their targets), and untyped custom models are now filtered by `isLLMModelID` (the same `embed|tts|speech|audio|voice|image|imagen|dall-e|flux|sdxl|sd-|stable-diffusion` heuristic as upstream's `inferKindFromUnknownModelId`).
  - The static dump only runs when the `providerConnections` table is genuinely empty (upstream `connections.length === 0`).
  - Non-LLM combos (`webSearch`/`webFetch`) are excluded from this list — the endpoint builds with `kindFilter ["llm"]` (upstream `comboMatchesKinds`); combo entries carry `capabilities` only, without `context_length`/`max_completion_tokens`.
- `internal/providers/registry_aliases.go` (new): 61 provider aliases ported from `open-sse/providers/registry` (`uiAlias || alias`), and `GetProviderAlias` now uses that table — providers without an upstream alias are published under their own id (`clinepass`, `nvidia`, `openrouter`, `openai`) instead of our invented short aliases. The old prefixes (`cp`, `or`, `nv`, `gb`) are gone from the listing; `ProviderAliasMap` (alias → id) still drives **request resolution**, so `cp/...` keeps routing to clinepass. The now-dead `ProviderToAliasMap` was removed.
- Entry shape aligned with upstream: `ModelInfoObject` now only has `id`, `object`, `owned_by`, `capabilities`, `context_length`, `max_completion_tokens` (the `created` and `context_window` fields that upstream does not have were dropped), and `CapabilitiesDetail` gained `search`, `tools`, `reasoning`, `thinkingFormat`, `contextWindow`, `maxOutput` (replacing the duplicated `contextWindows`).
- `internal/providers/registry_models.go` fully regenerated from `open-sse/providers/registry` (89 providers, 1550 id/alias entries) plus a new `ProviderModelKinds` table (191 media models typed `image`/`tts`/`stt`/`embedding`/`video`/`systemone`) and a `GetProviderModelKind` accessor. Kind resolution in `/v1/models` now mirrors upstream (custom `type` → registry `kind` → id heuristic), so media models no longer leak into the LLM list and the static catalogue tracks upstream (e.g. `openrouter` 10 → 18 entries, `ag` 21 models).
- Derived fix: a provider with one active plus one inactive connection row is no longer treated as entirely disabled (previously `tr/*`'s 153 models disappeared).
- Cross-check against the running upstream instance (port 20128, same database): **714 models upstream vs 726 in 9router-go, 687 identical ids**; prefixes, entry keys and `capabilities` keys match exactly. Breakdown: `clinepass` 469/469, `tr` 154/153, `kr` 34/44, `ag` 20/20, `cbai` 15/15, `openrouter` 7/6, `nvidia` 4/4, `Id` 3/3, `gcli` 1/5, combos 7/7.
- Remaining 39-id gap at that point: (a) upstream uses a **live catalog** for kiro/grok-cli (`kr/auto`, `kr/minimax-m2.1`, `gcli/grok-4.7`) while 9router-go used the static catalogue — the next PR; (b) `openrouter/free` exists upstream only; (c) an upstream quirk: vendor-prefixed ids (`nvidia/parakeet-ctc-1.1b-asr`) lose their qualifier before the kind lookup, so they slip through on both sides.
- Tests: `TestIsLLMModelID` (9 cases), `TestGetProviderModelKind` (11 cases), `TestHandleModels_MediaKindModelsExcluded`, `TestHandleModels_EnabledModelsOverrideCatalog`, `TestHandleModels_AliasTargetMergedIntoProvider`, `TestHandleModels_ComboOwnedByCombo`, plus the filtering tests from the previous commit; the legacy `TestHandleModelLookup_ProviderModel` / `TestHandleModels_IncludesTokenLimits` were re-seeded to upstream semantics (an alias needs a connected provider, and token limits are asserted through `context_length`/`max_completion_tokens`/`capabilities.contextWindow`). Full suite: 1433 pass.

### 🐛 Fallback logs never named the failing account/project (Antigravity 403 `VALIDATION_REQUIRED`)

- Symptom: `WRN [fallback] upstream failed provider=antigravity ... status=403 error=... "Verify your account to continue."` only printed `conn=<uuid>`, so there was no way to tell which project ID / email needed verification at `accounts.google.com`.
- `internal/handlers/chat/conn_identity.go` (new): `connIdentityKV` (name, email, projectId from `data.projectId` or `data.providerSpecificData.projectId`, empty fields skipped) + `connIdentityKVByID` (lookup via `Repo.GetProviderConnectionByID`, failure paths only).
- `internal/handlers/chat/fallback.go`: the `upstream failed` and `connection locked` logs now add `connName=`, `email=` and `projectId=`. Example: `... status=403 cooldown_s=120 connName=AG Verify email=luqmangeminipro@gmail.com projectId=mega-rainfall-szp2g`.
- Tests: `TestConnIdentityKV` (6 cases: both projectId shapes, empty fields, broken JSON, nil conn) + `TestConnIdentityKVByID_MissingConnection` (a handler without a Repo does not panic). Smoke: a mocked 403 `VALIDATION_REQUIRED` makes both log lines carry the email and project ID.

### 🐛 `POST /api/models/test` returned 401 even when logged in

- The route moved from the `RequireApiKey` group (Bearer / `X-API-Key` only) to `RequireDashboardAuth` — parity with upstream `dashboardGuard` (`src/app/api/models/test/route.js`). Previously a dashboard authenticated by cookie session got `401 invalid_api_key`; now cookie session, CLI token and API key are all accepted, matching `/api/models/custom|disabled|alias`.
- Regression test: `TestSetupServerRouter_ModelTestDashboardSession` (anonymous → 401, valid session → passes).

### 🐛 Kiro `400 Improperly formed request` — OpenAI body forwarded raw to the gateway

- Root cause: there was no OpenAI→Kiro translator in Go. `ForwardKiro` passed the OpenAI body through untouched; the kiro.dev gateway only accepts a `conversationState` envelope and answered `400 {"message":"Improperly formed request."}`. Upstream has `openai-to-kiro.js` / `claude-to-kiro.js`, which were not ported.
- `internal/translator/kiro.go` (new): port of `openaiToKiroRequest` — build the `conversationState` (chatTriggerType / conversationId / currentMessage / history), inject the system and time context into the user turn's content (the gateway rejects a top-level `systemPrompt`), tool_use / tool_result, base64 images → Kiro blocks, `profileArn` from `providerSpecificData`, `inferenceConfig`, `modelId` backfill across history, and merge consecutive user turns. Upstream session replay and thinking budget are not ported yet.
- `ForwardKiro` now runs the translator for OpenAI bodies and passes through bodies that already carry `conversationState` (MITM); it strips the `kr/` prefix and the `-thinking` / `-agentic` suffixes before `modelId`.
- Tests: `TestOpenAIToKiro_*` (7) + `TestKiroUpstreamBody_*` against a mock upstream (3).

### 🐛 Kiro connect modal never opened and the OAuth/API Key buttons were overwritten

- Root cause 1: `devicePollTimer` was never declared, so `stopDevicePoll()` threw a `ReferenceError` inside `openKiroOAuth()` and `showOAuthModal = true` never ran (no console error, because the failure was in an async callback).
- Root cause 2: `isNoAuth` was inferred from `category === 'free'`; kiro and gemini-cli declare `noAuth: false` but were still treated as no-auth, hiding the connection card (and its buttons). Upstream parity: only an explicit `noAuth` flag counts. Also fixed in `combos/pickerData.ts`.
- Root cause 3: the `{:else if providerId === 'kiro'}` branch in the lower button row swallowed the `{:else if hasDualAuthModes}` branch (the OAuth/API Key buttons). The "Waiting…" banner and the "MANUAL TOKEN IMPORT" block were hidden whenever the Kiro method list was shown.
- Tests: browser verification (click → 7 methods render, with no leftover generic markup).

- `web/vite.config.ts` — the dev proxy now also covers `/admin`; previously `resetHealth()` (`/admin/health/reset`) fell through to the SPA fallback in dev mode because it was not proxied to Go.
- `Makefile` — new `web-dev` target (Vite dev server on :5173 with HMR). Two-terminal workflow: `make dev` (Go on :20130) + `make web-dev` → frontend changes hot-reload without rebuilding or restarting the binary.
- `web/README.md` — documents the two-terminal dev workflow and the service worker caveat in dev (`sw.js` also registers in dev; hard-refresh / unregister if the view looks stale).

## [v1.9.2] - 2026-09-26

### 🐛 Dashboard console errors: version 401 spam, manifest/SW 404, missing icons, auth redirect

- `GET /version`, `/api/version`, `/api/version/status`, and `/api/version/check` are now public (upstream `PUBLIC_API_PATHS` parity): the Sidebar polls on every page including `/login` before any session exists. `POST update/shutdown/auto-update` stay admin-only (upstream `ALWAYS_PROTECTED`).
- `GET /sw.js`, `/manifest.webmanifest`, and `/manifest.json` routed to the embedded SPA handler; PWA shell files existed in `web/dist` but chi had no route.
- Added missing provider icons `opencode-zen.png` and `ollama-search.png`.
- Frontend 401 handling: `onUnauthorized` clears stale local session and redirects to login; `App` stops polling when logged out.
- `POST /api/models/test` moved to `RequireDashboardAuth` (cookie session, CLI token, and API key accepted) — dashboard got `401 invalid_api_key` despite being logged in.
- Sidebar nav now scrolls with sticky header/footer: `aside` is viewport-bounded (`h-full max-h-screen overflow-hidden`), header/footer `shrink-0`, nav `min-h-0` (closes issue #22 side-nav points; live-verified at 500px viewport + mobile drawer).
- Dev workflow: `make web-dev` (Vite :5173 + HMR, no binary rebuild); dev proxy keeps `/debug` (traces/pprof) alongside `/admin` (health reset).

### 🐛 Media providers audit (search/fetch/image/STT/TTS) + Antigravity failover

- Antigravity/Xquik search, Antigravity image/STT, and Nvidia TTS now rotate all active accounts with per-account `ClassifyError` locks and success unlocks; pinned `x-connection-id` honored once.
- Fixed Xquik registry `BaseURL`, clamped `max_results 5..100`, optional `queryType`, `answer`/timing envelope fields, upstream error statuses preserved.
- Request-scoped 4xx no longer lock accounts; combo video skips 5xx rotation; multipart model rewrite byte-exact; pinned connections provider-scoped.
- `GET /api/keys` returns full secrets to dashboard sessions (upstream parity); masked values stay for API-key callers.
- Live-verified: OpenRouter embeddings `:20128` vs `:20129` return byte-identical 3072-dim vectors.

## [v1.9.1] - 2026-09-25

### 🐛 Dashboard: Custom Models Parity — Combo Picker Unwraps `{models}` Envelope

- `web/src/lib/customModels.ts` (new): shared `parseCustomModelsResponse` (`{models:[...]}` upstream shape, tolerates bare-array/record-map), `parseDisabledModelsMap` (`{disabled:{...}}` upstream + bare map go-port), `notifyCustomModelsChanged` / `subscribeCustomModelsChanged` (`customModelChanged` + `focus` reload, ported from upstream `SttExampleCard.js` / `ModelsCard.js` / `useModelCaps.js`).
- `web/src/components/combos/ModelPickerModal.svelte`: `normalizeCustoms`/`normalizeDisabled` now use the shared parser. Root cause of the reported bug: `GET /api/models/custom` returns `{models:[...]}` but the modal expected a bare array/record, so `fetchedCustoms` stayed `[]` and `oc/space-bunny-free` (custom-only, not in builtin `oc` catalog) never appeared in "tambah combo" — while the provider page (which already unwrapped `{models}`) showed it.
- `web/src/api/client.ts`: `getCustomModels`/`getDisabledModels` typed honestly (`{models:[...]}` / map) so future consumers stop guessing the envelope.
- `web/src/components/connections/types.ts`: `fetchProviderModelsData` uses the shared parser (same result as before, single code path).
- `web/src/components/media/MediaProviderDetail.svelte`: models card merges builtin + custom per kind (upstream `ModelsCard kindFilter` parity, builtin dedupe). Previously custom media models (`tts`/`stt`/`image`/`embedding`/`video`) never appeared.
- `web/src/components/media/SttExampleCard.svelte`: custom STT filter matches `storageAlias || providerId` (upstream uses `getProviderAlias`), reloads on `focus` + `customModelChanged` like upstream `loadCustom`.
- `web/src/components/connections/ProviderDetailView.svelte`: dispatches `customModelChanged` after every add/delete/import of a custom model (upstream `ModelsCard`/`page.js` parity), so pickers and STT cards refresh without full reload.
- Unchanged (already parity): `TtsExampleCard` stays builtin-only like upstream; backend `GET /models/disabled` bare-map shape kept, parser handles both.


## [v1.9.0] - 2026-09-25

### 🔀 Routing: antigravity-prefixed muse-spark Reaches the Owning Executor

- `internal/handlers/chat/resolution.go`: `routeModelToOwningProvider` — a request like `ag/muse-spark-1.3-contributor-free` (prefix copied from a dashboard combo) now resolves to the provider that actually serves the model (opencode family) instead of antigravity, which answered upstream 404 `Requested entity was not found`. Native antigravity models are untouched. Ported from `fix/10` (`ad4b355b`), which never reached main. Tests: `TestRouteModelToOwningProvider`, `TestResolveModel_AntigravityMuseSparkRoutesToOpencode`.


### 🐛 Dashboard: Recent Requests List No Longer Blinks/Shrinks on First Request

- `internal/usagetracker/tracker.go`: seed the in-memory `recentRing` from `usageHistory` once per process (upstream `ensureRingInitialized` parity) + `recentFromHistoryRow` mapper. Previously a fresh process streamed a ring holding only post-restart rows, so the first completed request replaced the dashboard's DB-backed 20-row list with 1 row (list blinked, rows below vanished). Regression test `TestTracker_RingSeededFromHistoryOnce`.
- `web/src/components/analytics/AnalyticsView.svelte`: SSE `recentRequests` now merges (union + dedupe + newest-first + cap 20) instead of replacing, so a short stream payload can never drop rows already rendered.


### 🧹 Leak Hunt: 7 Fixes for 24/7 Operation (Independent Audit)

- `internal/shutdown/shutdown.go` + `internal/app/server.go`: new `shutdown.Context()` (canceled on `Cancel`); updater + catalog-sync loops take it instead of `context.Background()` — background goroutines + tickers now exit on ^C. Fixed `TestReset` double-close panic (recreate `done` channel).
- `internal/handlers/chat/combo_fusion.go`: `collectPanel`/`makePanelCall` take `ctx`; stragglers abort on grace/hard timeout, client cancel, or shutdown (previously `context.Background()`, orphaned until upstream responded).
- `internal/proxy/executor/freebuff_session.go` + `internal/handlers/chat/antigravity_quota.go`: lazy eviction of expired entries + opportunistic sweep (2× TTL) — token-rotated keys no longer accumulate.
- `internal/handlers/chat/connections_proxy.go`: `proxyClients` capped at 128 with idle-close eviction (each entry pins a Transport + sockets).
- `internal/auth/session.go`: login limiter capped at 5000 buckets with window sweep + oldest-evict (scanner IPs bounded).
- `internal/handlers/chat/gemini_handler.go` + `internal/proxy/executor/stream.go`: 10MB caps on non-stream body reads and codex SSE accumulation.
- Out of scope (pre-existing, bounded): HeartbeatWriter ticker (dies with stream Close), tracing ring (2000), translator prune (50/10min), MITM conns (Wait).


### 🔊 Opencode Responses Errors Fail Loud (No More Silent Empty 200)

- `internal/proxy/executor/stream.go`: `ProcessCodexEvent` now records upstream `{"type":"error",...}` events (e.g. `FreeTierError` on muse-spark `-free` models) on stream state; `handleCodexStream` (non-stream) converts them to `*proxy.UpstreamError` (403 for free-tier/auth gates, else 502) instead of emitting `200 + content:""`. Silent success broke agents, bypassed account fallback/locks, and faked green monitoring. Upstream Next.js (`base.js`) likewise returns non-OK responses as errors, never empty 200s.
- Live evidence: `POST opencode.ai/zen/v1/responses` with `muse-spark-1.3-contributor-free` returns `FreeTierError: "OpenCode's free tier can only be used from within OpenCode"`.
- Limit (honest): on the already-committed SSE stream path headers cannot be unwound, so a pure-error stream still closes without chunks; the non-stream path (which agents use for the failing case) now errors properly.


### 🔗 Freebuff Cross-Process Session Coordination (Anti-Hijack)

- `internal/proxy/executor/freebuff_session.go` + `freebuff.go`: session lookup now memory L1 → `upstream_leases` L2; admission is coordinated — exactly one claimer per token+model across processes sharing the DB (`freebuffClaimMu` in-process + `AcquireLease` cross-process). Losers follow the winner's `instanceId` instead of POSTing their own claim (the pattern upstream punishes with 409 `session_superseded`). Stale-session retry drops the lease compare-and-delete (a sibling's fresh row survives). `Request.Leases` (nil = memory-only, old behavior) wired from chat fallback ×2, media `/responses`, and the session-switch endpoint.
- Tests: two racers converge on 1 POST (fake + real SQLite backends), follower reads with 0 POST, stale drop is compare-and-delete, nil-store contract unchanged.
- Note: coordination fixes *technical* hijacking between cooperating instances, not *policy* — two machines serving traffic concurrently on one account is still concurrent use server-side.


### ⬆️ Upstream v0.5.86 Parity (decolua/9router#v0.5.86)

- `internal/handlers/chat/claude_cloaking.go` + `internal/providers/providers.go`: bumped Claude CLI fingerprint `2.1.258` → `2.1.280` (upstream `cbffeb9`), so the billing-header cloak and `claude-cli/*` UA stay current. Added `claude-opus-5-5` to `cc`/`claude` catalogs (`registry_models.go`, `web/src/lib/models.ts`).
- `internal/handlers/media/deploy.go`: Vercel relay template now forwards headers losslessly (copy to plain object, strip only `x-relay-target`/`x-relay-path`/`host`) — upstream `6af26a9`. Cloaking headers survive relay pools, which matters for proxied Freebuff traffic.
- Deferred: Xiaomi MiMo v2.6 desktop login (5 region clusters, dual-route models, server-assisted flow) — large scope, tracked as separate stacked diff.

### 🛡️ Freebuff client_id Cloaking (Anti-Ban Parity)

- `internal/proxy/executor/freebuff.go`: `ForwardFreebuff` reuses the account's stored `fingerprintId` verbatim as `codebuff_metadata.client_id`, falling back to a fresh unbranded UUID only for connections saved before this change. Previously every chat request sent `client_id: "9router-<uuid>"`, which brands the traffic as non-CLI at the application layer — the most likely reason accounts got `banned` even though headers/User-Agent already matched the CLI. Note: cloaking only removes the self-identifying fingerprint; it cannot protect accounts banned for quota abuse, multi-account farming on one IP/fingerprint, or region violations.
- `internal/handlers/oauth/cline.go`: `decodeClineCode` now accepts the real browser-callback shape — base64url (`-`/`_` alphabet, padding stripped) plus the trailing signature segment the extension appends after the JSON payload. Previously only strict `StdEncoding` decoded, so pasting the callback failed to extract tokens and the handler fell through to `POST /api/v1/auth/token`, which the server rejects with `Forbidden` — exactly the reported `Cline token exchange failed ... Forbidden` error.
- `internal/handlers/chat/connections.go` + `internal/handlers/dashboard/connection_probe.go`: the `workos:` prefix is now JWT-only (WorkOS JWT = base64url `eyJ…` + dot, upstream parity `open-sse/shared/clineAuth.js`). Non-JWT ClinePass API keys ride plain `Bearer` — prefixing them is what the server answers with 401 `"Unauthorized: Please make sure you're using the latest version of Cline and re-authenticate your Cline account."` Note: your pasted bundle decodes to a real WorkOS JWT (`eyJhbGciOiJSUzI1NiIsImtpZCI6InNzb19vaWRj...`, `expiresAt` already past `2026-09-24T07:23:51Z`), so that specific token is expired server-side — reconnect with a fresh browser login after updating.
- `internal/handlers/oauth/cline.go`: new Cline/ClinePass connections are named by account email from the token bundle (fallback: first+last name, then provider default) instead of the generic `ClinePass` label, so multi-account setups stay distinguishable in provider detail.
- All-providers branding sweep (no behavior change otherwise): audited every header/body sent to upstream. Only one real leak found and removed — `User-Agent: 9router/oauth` on the Antigravity Google token exchange (`internal/handlers/oauth/antigravity.go`), now unbranded Go default. Everything else already mirrors an official client: Freebuff `codebuff-cli/*` + fingerprinted `client_id` (no `9router-` anywhere on the wire), Cline `Cline/*` + `cline-cli`, Antigravity `antigravity/ide/*`, Gemini CLI `google-api-nodejs-client/*`, Grok/Codex/iFlow/Qoder/MiMo browser or CLI UAs, TTS browser UAs. `X-Msh-Platform: 9router` (Kimi) and `HTTP-Referer/X-Title: endpoint-proxy.local / Endpoint Proxy` (OpenRouter/Airforce) are byte-identical to upstream `decolua/9router` — changing them would *break* parity, not improve stealth. `User-Agent: 9Router` only ever hits the user's own proxy-test target and GitHub API (never an LLM provider). Local-only strings (`9router-oauth` BroadcastChannel, MITM CA, updater UA, file paths) never leave the machine.
- `internal/handlers/oauth/naming.go` (new) + all OAuth handlers (`freebuff.go`, `cline.go`, `antigravity.go`, `trae.go`, `windsurf.go`, `zed.go`, `authcode.go`, `pkce.go`, `device.go`): single email-first naming rule `connectionDisplayName` — account email when known, else explicit user-supplied name, else provider default. No more `"Provider (name)"` labels anywhere, so every provider detail page (Freebuff, ClinePass, Antigravity, …) lists accounts by email.
**Production Internet Hardening & Cloudflare Integration:**
- **Protect `/debug/pprof/*` endpoints**: Disabled Go runtime profiling endpoints (`/debug/pprof/*`) by default in production to prevent Denial of Service (DoS) and potential memory/key disclosures. Can be explicitly enabled via `PPROF_ENABLED=true`.
- **Privilege separation for client API keys**: Restricted destructive administrative routes (`/api/version/shutdown`, `/api/version/update`, `/api/settings/database`, `/admin/health/reset`) so they strictly require a valid dashboard JWT session cookie or local CLI token (`x-9r-cli-token`), matching upstream `ALWAYS_PROTECTED` behavior in `src/dashboardGuard.js`. Client API keys can no longer trigger shutdowns or database dumps.
- **Cloudflare `CF-Connecting-IP` support**: Updated `LoginClientIP` in `internal/auth/session.go` to support `CF-Connecting-IP` when `TRUST_PROXY=true` or `TRUST_CLOUDFLARE=true`, ensuring proper client IP resolution and preventing shared-bucket lockout behind Cloudflare.
- **Configurable host binding (`HOST` / `BIND_ADDR`)**: Added `Host` to configuration and updated server listener to bind to `HOST` or `BIND_ADDR` when specified (e.g. `127.0.0.1` when proxied by `cloudflared`), while preserving `:20130` (`0.0.0.0`) default behavior.

### 🎨 UI & Dashboard

**Media Providers Full Parity (`/dashboard/media-providers/*`):**
- `internal/handlers/media/tts_synthesizers.go` & `tts_forward.go`: Built local native synthesis engines (`edge-tts`, `google-tts`, `nvidia`) and voice catalog endpoints (`/api/media-providers/tts/voices`), supporting real-time streaming audio generation and custom speed/pitch/voice options.
- `internal/handlers/media/antigravity_image.go` & `antigravity_stt.go`: Added Antigravity image generation and speech-to-text (STT) transcription handlers with multipart form-data parsing, extracting audio models and delegating to Google's IDE backend.
- `internal/handlers/media/antigravity_search.go`: Ensured typed JSON serialization for Antigravity web search requests and responses to match upstream key ordering and structure.
- `internal/handlers/chat/resolution.go`: Fixed System One endpoint `/v1/systemone` to handle `x-antigravity-session` headers and fall back seamlessly to direct connections with `public` default keys for `antigravity-zen`.
- `web/src/components/media/MediaKindView.svelte`, `MediaProviderCard.svelte`, `NoAuthProxyCard.svelte`, `TtsExampleCard.svelte`, and `SttExampleCard.svelte`: Full Svelte 5 runes parity with upstream Next.js for all 8 media kinds (`video`, `stt`, `tts`, `image`, `embedding`, `systemone`, `webSearch`, `webFetch`), aligning provider sorting, priority, hidden flags, and live audio/waveform test players.

**Antigravity Live Models Discovery & "Import from /models":**
- `internal/providers/registry_models.go` & `web/src/lib/models.ts`: Added Google Antigravity official live models (`gemini-2.5-flash`, `gemini-2.5-flash-lite`, `gemini-2.5-pro`, `gemini-2.5-flash-thinking`, `gemini-3.1-pro-high`, `gemini-3.1-flash-lite`, `gemini-3.5-flash-lite`).
- `internal/handlers/dashboard/connections.go`: Extended `GET /api/providers/:id/models` to support Antigravity, Gemini CLI, Cline, and ClinePass connections. For Antigravity, queries Google's live RPC (`https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels`) with Bearer tokens, filtering internal chat IDs and parsing vision/reasoning capabilities.
- `web/src/components/connections/ProviderDetailView.svelte`:
  - Added `[📥 Import from /models]` button next to `Add Model` for Antigravity, Cline, ClinePass, and Qoder accounts.
  - Auto-fetches live models from Google for active Antigravity connections on page load, displaying uncataloged models in the **Suggested models** section for 1-click addition.

**Suggested Free Models Feed & Custom Models Parity:**
- `internal/handlers/suggestedmodels.go`: Wrapped HTTP client in `proxy.NewFallbackTransport` so public model catalog feeds (`opencode`, `openrouter`, `kilocode`, `airforce`) never get blocked by sandbox proxy allowlists.
- `web/src/lib/providers.ts`: Added `modelsFetcher: { url: "https://opencode.ai/zen/v1/models", type: "opencode-free" }` to `opencode`, restoring the "Suggested free models (≥200k context)" section on OpenCode Free.
- `internal/handlers/dashboard/models.go`: Updated `GET /api/models/custom` to return `{ "models": [...] }` matching upstream Next.js shape, and updated `web/src/components/connections/types.ts` to cleanly parse custom model lists without type-assertion errors.

**Universal Outbound Direct Fallback & Proxy Allowlist Bypass:**
- `internal/proxy/fallback_transport.go`: Created `FallbackTransport` which wraps Go HTTP round-trippers to detect local proxy refusal (`403 Forbidden`, `blocked-by-allowlist`, `CONNECT tunnel failed`, or proxy text/plain errors) and instantly re-issue the request directly (`Proxy: nil`) with re-readable request bodies.
- Applied universally across chat resolution, streaming SSE forwarders, media endpoints, validation probes, and catalog feeds.

**Comprehensive Connection Health Probing & Zero False Errors:**
- `internal/handlers/dashboard/connection_probe.go`:
  - Added native probe configurations for OAuth providers (`antigravity`, `gemini-cli`, `cline`, `clinepass`, `freebuff`, `xai`, `grok-cli`, `codebuddy-intl`, `zed`, `windsurf`, `trae`, `devin`, `devin-cli`, etc.).
  - Implemented token-exists heuristic for unconfigured providers and compatible base-URL probing for custom nodes.
  - Fixed `persistProbeResult` so that informational "Provider test not supported" messages no longer falsely mark connections as `testStatus: "error"` or pollute `lastError` in the SQLite database.
  - Updated Cline / ClinePass probe to prefix WorkOS JWT tokens with `workos:`.

**Quota Tracker Full Parity with Upstream Next.js (`/dashboard/quota`):**
- `internal/handlers/router.go`: Mounted `/api/usage/{connectionId}`, `/api/usage/providers`, `/api/usage/stream`, `/api/usage/stats`, and `/api/usage/request-details` inside `SetupDashboardRoutes` with `RequireDashboardAuth`, allowing authenticated browser sessions (JWT cookie), local CLI tokens, and client API keys to fetch quota and usage details.
- `internal/handlers/dashboard/usage_providers.go`: Enhanced `fetchAntigravityDashboardWeekly` to parse both session (5h) and weekly quota buckets from Google's `retrieveUserQuotaSummary`, and added reconciliation when all Gemini models are exhausted (matching upstream `open-sse/services/usage/antigravity-weekly.js` and `google.js`).
- `web/package.json` & `web/src/main.ts`: Added and bundled `material-symbols/outlined.css` locally, ensuring all icons (refresh, edit, delete, eye-off, hourglass, toggle) render instantly and work 100% offline without text flashing.
- `web/src/components/quota/types.ts`: Implemented full upstream provider quota parser `parseQuotaData` supporting Antigravity (5-quota family grouping: Gemini 5h, Claude & GPT 5h, Gemini 3.1 Flash Image, Gemini Weekly, Claude & GPT Weekly), Codex, Kiro, Qoder, Claude, DeepSeek, Groq, Ollama, and Zed, with model catalog canonical sorting.
- `web/src/components/QuotaTrackerView.svelte`:
  - Added secondary connection label (`getConnectionSecondaryLabel`) for accounts with different emails/display names.
  - Aligned status badges to only render on Kiro connections (matching upstream Next.js).
  - Added connection edit action (pencil icon) with interactive `Edit Connection` modal (name & priority editing + reachability test).
  - Added auto-ping toggle (`bolt` icon) for Claude & Codex OAuth accounts and Codex reset credits integration.


**Token Saver Full Parity with Upstream Next.js (`/dashboard/token-saver`):**
- `internal/handlers/router.go`: Mounted `/api/headroom/*` (`status`, `start`, `stop`, `restart`, `extras`, and `proxy`) inside `SetupDashboardRoutes` with `RequireDashboardAuth`, allowing dashboard session cookies to query and control Headroom proxy lifecycle.
- `web/src/api/client.ts`: Updated `HeadroomStatusResponse` and `HeadroomExtrasResponse`, adding `getHeadroomExtras()`, `startHeadroom()`, `stopHeadroom()`, `restartHeadroom()`, `installHeadroomExtras()`, and `uninstallHeadroomExtras()`.
- `web/src/components/TokenSaverView.svelte`:
  - Removed duplicate in-page header (`PiggyBank` banner) to align with upstream Next.js header layout where page title & icon live in `TopBar`.
  - Replaced custom dialog with standard `Modal.svelte` featuring macOS-style traffic lights (`#FF5F56`), backdrop blur, and `Button.svelte`/`Input.svelte` components.
  - Implemented full Headroom status detection (`Checking…`, `Running`, `Not installed`, `Stopped`, `External`) with local/managed PID checks.
  - Added Headroom compression extras (`[code]`, `[ml]`), install confirmation modal (warning on 1GB ML download), pip uninstall, and log tail streaming.
  - Added locale-based Wenyan level filtering for Caveman output compressor (only showing classical Chinese compression levels on `zh` locales).


**Freebuff Multi-Account Session Management & Direct Proxy Fallback:**
- `internal/proxy/executor/freebuff_session.go` & `freebuff.go`: Added `DoFreebuffHTTP` with automatic fallback to direct connection (`Proxy: nil`) when local or environment proxies refuse requests to `codebuff.com` / `freebuff.com` (`403 Forbidden` / `blocked-by-allowlist`), preventing session lookups and admissions from failing.
- `internal/handlers/oauth/freebuff_session.go` & `freebuff_session_switch.go`: Automatically syncs active session models (`currentModel`) into `conn.Data` (`freebuffModel` and `assignedModel`) in SQLite, enabling strict model routing per connection.
- `web/src/components/connections/FreebuffSessionBanner.svelte`: Added multi-account selection pills allowing users to view and switch between different Freebuff accounts and their respective sessions directly from the banner.
- `web/src/components/connections/ProviderDetailView.svelte`: Added per-connection session badges (`🔒 model-id`, `queued`, `banned`, `no session`) on connection cards with a dedicated **Session** button (`lock_clock`) to quickly focus and manage any account's active seat.

**Vision & Audio Adapter Full Parity (`/dashboard/combos`):**
- `web/src/lib/models.ts`:
  - Updated `getModelCaps` to detect `audioInput` capability and refined vision detection by eliminating false positives from bare `"flash"` model ID tokens (which previously caused non-vision models like `deepseek-v4-flash` to be incorrectly classified as vision-capable).
  - Aligned vision patterns with upstream `visionPatterns.js` (`looksLikeVisionModel`), filtering out audio/tts/stt/embedding generators while identifying multi-modal vision families (`gemini`, `4o`, `gpt-5`, `gpt-6`, `opus`, `sonnet`, `haiku-4.5`, `fable`, `kimi`, `minimax`, `mimo`, `qwen`, `grok`, `llama-4`, `muse-spark`).
- `web/src/components/combos/pickerData.ts`:
  - Added `caps.audioInput` to `PickerModel`.
  - Strictly enforced modality filtering in `resolveFilteredGroups`: `target === 'vision'` now filters exclusively for models with `caps.vision === true`, and `target === 'audio'` filters exclusively for `caps.audioInput === true`, regardless of whether a search query is active.
  - Added empty state handler displaying `No models found` with search icon when no models in the active catalog match the modality requirement.
- `web/src/components/combos/CapacityAdapterSection.svelte`:
  - Removed redundant summary bullet list above cards to match upstream Next.js header layout.
  - Aligned subtitles to `— images (png, jpg, webp, …)` and `— audio input`.
  - Standardized icons to Material Symbols `visibility` (eye) and `graphic_eq` (sound wave equalizer).
- `internal/db/settings.go`: Added `CapacityAdapterEntry` and `CapacityAdapter map[string]CapacityAdapterEntry` to `SettingsData` with default fallback to `ag/gemini-3.8-flash-high`.
- `internal/handlers/chat/combo.go`: Implemented `AugmentModelsWithCapacityAdapter` to automatically prepend models from the capacity adapter pool when none of the target models support required input modalities (e.g. vision for image inputs).
- `internal/handlers/chat/chat.go`: Integrated capacity adapter auto-switch into both OpenAI `/v1/chat/completions` and Anthropic `/v1/messages` for both combos and single-model requests.
- `internal/handlers/chat/vision_adapter_e2e_test.go`: Added comprehensive E2E tests verifying automatic switching from text-only models (`deepseek/deepseek-chat`) to vision-capable models (`ag/gemini-3.8-flash-high`) when image inputs are present in OpenAI and Anthropic request formats.

**Change Log in-app modal (upstream Next.js parity):**
- `web/src/components/ChangelogModal.svelte`: Added `ChangelogModal` Svelte 5 component with markdown parsing via `marked`, custom scrollable container, loading/error states with retry, backdrop-blur overlay, and links to GitHub Releases & Changelog history.
- `web/src/components/TopBar.svelte`: Changed the App Drawer item under "Theme" from an external link to a button triggering the in-app `ChangelogModal`, matching upstream Next.js `HeaderMenu` behavior.
- `web/src/api/client.ts`: Added `api.getChangelog()` querying `/api/changelog` with fallback to GitHub raw URLs.
- `internal/handlers/chat/chat.go` + `internal/handlers/router.go`: Added `GET /api/changelog` and `GET /changelog` endpoints to serve the application changelog directly from local disk with remote fallback.
- `web/src/index.css`: Added `.changelog-body` markdown typography and badge styles.

**Update notification banner in Sidebar (upstream Next.js parity):**
- `web/src/components/Sidebar.svelte`: Added update notification banner below the version label (`↑ New version available: v{latestVersion}`) with `Update now` button and clickable `9router-go update` command pill, matching upstream Next.js `Sidebar.js`.
- Added interactive Update modal with release notes, one-click auto-updater (`api.triggerUpdate()`), manual copy command with countdown shutdown, and disconnected reconnect overlay.
- `web/src/api/client.ts`: Added `SystemVersionInfo` type, `checkUpdate()`, `triggerUpdate()`, and `shutdownServer()`.

**Remove 9Remote & 9English; align Support 9Router with 9router-go repo:**
- `web/src/components/Sidebar.svelte`: Removed the `9Remote` action button & modal and `9English` external link from navigation items; cleaned up modal markup and unused `isRemoteModalOpen` state.
- `web/src/components/TopBar.svelte`: Updated the "Support 9Router" modal to remove the external 9English project link and point to the `9router-go` repository ([`https://github.com/luqman-v1/9router-go`](https://github.com/luqman-v1/9router-go)) and Releases page ([`/releases`](https://github.com/luqman-v1/9router-go/releases)).

### 🐛 Bug Fixes

**OpenCode Chat Completions Tool Fingerprint & Model Purity (`space-bunny-free`):**
- `internal/translator/fingerprint.go`: Fixed `ConcealFingerprintTools` to preserve standard Chat Completions tool shape (`{"type":"function","function":{"name":...}}`) with `"tool_choice":"none"` when client tools are absent, instead of falling back to flat Responses format (`{"type":"function","name":...}`) which caused upstream `[invalid_request_error] invalid request` on OpenCode Chat Completions models like `space-bunny-free`.
- `internal/proxy/executor/providers.go`: Stripped provider prefixes (`oc/`, `opencode/`) from `model` in `ForwardOpencode` and `ForwardOpencodeGo` before forwarding upstream.
- `internal/handlers/chat/resolution.go`: Removed hardcoded model rewrites (`strings.Contains(model, "muse-spark")`, etc.) from Antigravity resolution; all `ag/` and `antigravity/` models resolve cleanly to `antigravity` without special-case overrides.
- `web/src/components/connections/ProviderDetailView.svelte`: Restricted `suggestedModels` strictly to providers that declare a public `modelsFetcher` (upstream Next.js parity). Removed artificial suggested models injection under Antigravity so OpenCode models no longer leak into Antigravity's view.

**Antigravity Google OAuth Callback percent-encoding unescape:**
- `internal/handlers/oauth/antigravity.go`: Added `cleanAuthCode` to unescape double-encoded slashes (`4/0A...` vs `4%252F...`) and strip raw URL parameter prefixes before submitting `application/x-www-form-urlencoded` token exchange requests to Google.
- `web/src/components/connections/ProviderDetailView.svelte`: Added `decodeURIComponent` input cleansing for pasted OAuth authorization callback URLs.

**Deep Auth Verification for OpenAI-Compatible Custom Nodes:**
- `internal/handlers/dashboard/validate.go`: Added a secondary 1-token probe to `POST /v1/chat/completions` during provider node validation (`validateOpenAICompatibleNode`), preventing mock or unauthenticated servers from returning false positive validation results.

**Fix Round-Robin routing for combos and provider connections (Issue #20):**
- `internal/db/settings.go`: Updated `SettingsData` and `GetSettings()` to parse both dashboard JSON keys (`fallbackStrategy` / `rotateStrategy` and `stickyRoundRobinLimit` / `stickyLimit`), as well as global `fallbackStrategy`, `stickyRoundRobinLimit`, `comboStrategy`, `comboStickyRoundRobinLimit`, and `comboStrategies`.
- `internal/db/settings.go`: Updated `SetProviderStrategy` and added `SetComboStrategy` to write to `settings.data` via `UpdateSettingsRaw` without clobbering other settings fields.
- `internal/db/repos.go`: Updated `GetComboByName`, `GetComboById`, and `GetCombos` to populate `combo.Strategy` from `settings.comboStrategies[combo.Name]` and global `settings.comboStrategy`.
- `internal/handlers/chat/resolution.go`: Added `resolveComboRouting` so `ResolveModel` and `resolveModelEntry` populate `ModelInfo.Strategy`, `ModelInfo.StickyLimit`, and `ModelInfo.JudgeModel` from `settings.comboStrategies` or global combo settings.
- `internal/handlers/chat/connections.go` & `fallback.go`: Updated provider connection selection to rotate active connections using `fallbackStrategy` or global fallback settings when configured to `"round-robin"`.
- Added unit tests in `internal/db/settings_test.go` and `internal/handlers/chat/connection_strategy_test.go` covering combo strategy resolution, sticky limits, judge models, and provider connection rotation.

**Fix fetch stream double-read in `web/src/api/client.ts` and add missing tunnel endpoint handlers:**
- `web/src/api/client.ts`: Resolved `Failed to execute 'text' on 'Response': body stream already read` error when receiving non-2xx responses. Previously, `res.json()` consumed the stream body on error responses, which caused the subsequent `res.text()` fallback in the catch block to crash. The client now safely reads `res.text()` first before attempting JSON parsing.
- `internal/handlers/dashboard/tunnel.go` & `internal/handlers/router.go`: Added endpoints `POST /api/tunnel/enable`, `POST /api/tunnel/disable`, `GET /api/tunnel/tailscale-check`, `POST /api/tunnel/tailscale-enable`, and `POST /api/tunnel/tailscale-disable` with structured JSON responses and clean error handling instead of unhandled 404s.

**Combo bypass Vercel Edge Relay for no-auth providers (e.g. `oc/muse-spark-1.3` 429 on `combo-wombo`, solo test 200):**
- `internal/handlers/chat/combo.go` (chat + messages fallback) and `combo_fusion.go` — no-auth branch now sets `ProxyPoolID: h.ResolveProviderProxyPoolID(modelInfo.Provider)` (was `&ConnectionData{APIKey}` only), so combo routing goes through the configured `providerStrategies.<provider>.proxyPoolId` relay (`x-relay-target`/`x-relay-path`) exactly like the solo path (`handleAccountFallback`). Direct-to-`https://opencode.ai/zen/v1/responses` calls that burned the free-tier IP quota (`FreeUsageLimitError` 429) are eliminated.

**Vercel Edge Relay header forwarding for `muse-spark` / `antigravity`:**
- `internal/proxy/opencode.go` — `BuildOpenCodeHeaders` preserves `x-relay-target` / `x-relay-path` instead of dropping them.
- `internal/proxy/executor/providers.go` — `ForwardOpencode` / `ForwardOpencodeGo` keep `BaseURL` on the relay host and route via `x-relay-path` (`/zen/v1/responses`, `/zen/v1/messages`, `/zen/go/v1/responses`) when relay headers are present.
- Added `TestForwardOpencode_MuseSpark_EdgeRelay` (PASS); verified live `200 OK` via relay.

**Topology false pulse on dashboard load (`AnalyticsView.svelte`):**
- SSE `/api/usage/stream` initial snapshot no longer triggers the electric-beam animation: added `streamInitialized` guard so only genuine new model requests after init pulse; active-request updates set `lastProvider` without re-pulsing. Dashboard API traffic (`/api/usage`, polling) never triggers topology effects — only upstream model calls do.
- Consolidated per-node SVG turbulence filters into one lightweight global filter (`numOctaves="1"`) for GPU/CPU relief during continuous animation.

**Query-param auth for SSE streams (`internal/middleware/auth.go`):**
- `ExtractApiKey` accepts `?key=` / `?apiKey=` on routes ending in `/stream` (native `EventSource` can't set custom headers); REST/LLM endpoints stay header-only.

**Topology Option A visuals (`ProviderTopologyCard.svelte` + `web/src/index.css`):**
- Bidirectional neural stream (cyan prompt Router→Provider, emerald/gold response Provider→Router), dual shockwave rings on the active provider target, router absorption rings, node micro-bounce + `LIVE` badge.

### 🔄 Upstream Parity Sync

**Strike-breaker quota-only (upstream `decolua/9router#4197` parity, PR #16):**
- `internal/handlers/chat/antigravity_quota.go` — `HandleAntigravityQuotaError` now takes the upstream `errorMessage` and only counts a strike on explicit quota markers (`RATE_LIMIT_EXCEEDED`, `QUOTA_EXHAUSTED`, `Individual quota reached`). Generic bare `RESOURCE_EXHAUSTED` 429s no longer burn strikes / lock combos.
- `internal/handlers/chat/gemini_handler.go` — forwards `string(uErr.Body)` as the error message source.
- Added `TestAntigravityQuota_Generic429NoStrike` regression test.

**Refusal → content_filter mapping (upstream `decolua/9router#4210` parity, PR #16):**
- `internal/translator/claude_response.go` — streaming + non-streaming: Gemini `refusal` finish maps to `content_filter`, emits `stop_details.explanation` so Claude Code renders the block instead of hanging.
- `internal/translator/response.go` — reverse mapping `content_filter → refusal` for round-trips.
- Added refusal stream / non-stream / round-trip tests.

**Weekly vs session quota buckets (upstream `decolua/9router#4209` parity, PR #18):**
- `internal/handlers/chat/antigravity_quota.go` — `ParseWeeklyQuotaSummary` classifies the `window` field into weekly (`gemini`/`claude_gpt`) vs 5h-session (`gemini_session`/`claude_gpt_session`) buckets; `IsAntigravityModelBlocked` honors session buckets via `quotaEntryExhausted` helper.
- Added `TestAntigravityWeeklyQuota_SessionBuckets`.

**Add Compatible modal + provider-node validation (upstream `AddCompatibleModal.js` + `provider-nodes/validate/route.js` parity):**
- `web/src/components/connections/AddCompatibleNodeModal.svelte` — rebuilt to match the upstream modal: separate `Name` / `Prefix` / `API Type` fields with upstream placeholders (`OpenAI Compatible (Prod)`, `oc-prod`, …) and hints, `API Key (for Check)` + `Model ID (optional)` inputs driving a `Check` button with `Valid` / `Invalid` badges (chat-fallback note included), and full-width `Create` + `Cancel`. The API key is now validation-only and no longer auto-creates a connection (`ConnectionsView.svelte`).
- `internal/handlers/dashboard/provider_nodes.go` + `router.go` / `routes.go` — added `POST /api/provider-nodes/validate` (OpenAI-compatible `/models` + chat fallback, Anthropic-compatible with `x-api-key` + `/messages`-suffix strip, `custom-embedding` with dimension report), including SSRF guard for non-loopback callers (`handlerutil.AssertPublicURL`).
- `web/src/api/client.ts` — added `validateProviderNode`.
- Added `provider_nodes_validate_test.go` (13 tests: input guards, SSRF/local, OpenAI/Anthropic/embedding probes, chat fallback, network-error mapping).

### 🧹 Style Cleanup (behavior-neutral, PR #17 + follow-ups)

- `interface{}` → `any` across production code and test files; `errors.New` + `%w` wrapping; `slices.Contains/Sorted/Delete`, builtin `max()`/`clear()`, `strings.Builder`, shared header constants in `internal/constants`.
- Named constants: `antigravityDecoyUnavailable`, `maxReadLimit`, `thinkingHeadroomTokens`, `maxCallIDLen`, `MaxUpstreamBodyBytes`/`UpstreamErrLimit`.
- `fallback.go` — `forwardRequestParams` struct replaces 10-param forwarding; `openai.go` — `sseStreamOpts` struct replaces 8-param SSE helper.
- `antigravity_quota.go` — `AntigravityQuotaError` struct + named quota markers (replaces `map[string]any` error plumbing).
- Added `samber/lo` (`Ternary`, `CoalesceOrEmpty` only — `Coalesce` on `any` maps and eager `Ternary` slicing deliberately avoided).
- Default port `20128` → `20130` (`config.go`, `Makefile`, `mitm/handlers/base.go`, `.env.example`, `docker-compose.yml`, `Dockerfile`, `README.md`).

### 🧪 Tests

- `gemini38_live_test.go` — real upstream tests for `ag/gemini-3.8-flash-medium` (chat + stream, `200 OK`). Live E2E: 17/17 PASS.

### 📦 Release Hardening (issue #19)

- `make cross` generates `SHA256SUMS.txt`, uploaded by `release.yml`; README documents the Windows Defender false-positive (`Wacatac.C!ml` heuristic on the unsigned binary) with verify + Allow steps.


## [v1.8.18] — 2026-09-21

### 🐛 Bug Fixes

**OMP Harness False-429 on Antigravity (upstream `decolua/9router#3986` parity):**
- `internal/translator/antigravity.go` — `WrapForAntigravity` no longer sends `requestType: "agent"` in the Cloud Code envelope (`AntigravityRequest.RequestType` is now `omitempty` and left empty). Google enforces a tiny separate quota bucket whenever `requestType="agent"` is present, so OMP (Oh My Pi) harness payloads (~25–30k token system prompt + tools) were rejected with false `429 RESOURCE_EXHAUSTED` even with quota remaining — cascading into `CACHE_BLOCK` account locks while Claude Code stayed green. Verified live: same payload returns `200 OK` after the fix.
- `internal/translator/antigravity_test.go` — Added `TestWrapForAntigravity_OmitsAgentRequestType` regression test (asserts the field is absent from the raw envelope JSON and the `requestId` `agent/<…>` shape is preserved). Image (`image_gen`) and search (`search`) request types are untouched.

### 🔄 Upstream Parity Sync — `decolua/9router` v0.5.75…v0.5.81 (100%)

**Model Catalog & Routing:**
- `internal/providers/registry_models.go` — Registered `deepseek-v4.1-flash` for `codebuddy-intl` (`cbai`, replacing the retired `deepseek-v4-flash`) and added `deepseek-v4.1-flash:cloud` to the `ollama` catalog.
- `internal/handlers/chat/resolution.go` — Routed bare `codex-auto-review` to the `codex` provider (PR #4135 parity), resolved even with a nil repo / empty DB.

**Antigravity Hygiene:**
- `internal/translator/antigravity.go` — Stripped the Claude Code `x-anthropic-billing-header` from system prompts and sanitized the Hermes Agent identity (`You are Hermes Agent, an intelligent AI assistant created by Nous Research.` → neutral form) to eliminate false HTTP 429/403 anti-abuse rejections.
- `internal/translator/thought_signature_store.go` — Scoped cached Gemini thought signatures to the producing model family (`claude` vs `gemini`), preventing cross-family replay that triggers HTTP 400 `Invalid thought signature` when a conversation switches models (upstream `bc3be0cb` parity).
- `internal/translator/gemini.go` — Threaded the model name through the `GetGeminiThoughtSignature` / `StoreGeminiThoughtSignature` call sites so stored signatures are keyed per model family (call-site half of the scoping above).

**CommandCode Multimodal & Reasoning:**
- `internal/proxy/executor/providers.go` — Added native image blocks to `buildCommandcodeBody`: OpenAI `image_url` data URIs and Claude/OpenAI base64 image sources are converted to CommandCode `{type: "image", image: <dataUri>, mimeType}` blocks, and `reasoning_effort` (`low`/`medium`/`high`/`max`) is preserved on `/alpha/generate`.

**Union-Alpha / OpenCode Parity (verified):**
- Confirmed live routing of `oc/union-alpha` through the Anthropic Messages API (`/zen/v1/messages`) with `anthropic-version: 2023-06-01` and automatic `max_tokens` injection (PR #4099 parity); free-tier `forceStream`/SSE aggregation parity already structural in Go.
- `internal/handlers/chat/muse_spark_e2e_test.go` — Added `TestIntegration_OpenCode_UnionAlpha_Messages` (live E2E; SKIPs on upstream rate-limit or auth-dependent `Model union-alpha is not supported` 401, consistent with existing Muse Spark E2E policy).

## [v1.8.17] — 2026-09-18

### 🚀 Features & Upstream Parity

**Claude OAuth Subscription (`sk-ant-oat`) Support End-to-End (PR #13):**
- Contributed by **@rezhajulio** ([#13](https://github.com/luqman-v1/9router-go/pull/13)) — Special thanks for bringing full Claude Pro/Max subscription parity from the dashboard to the native Go proxy!
- `internal/handlers/chat/fallback.go` — Automatic header switching to `Authorization: Bearer` and appending `?beta=true` for Claude OAuth credentials (`sk-ant-oat` or `accessToken`), supporting direct Anthropic API as well as Edge Relay proxy pools.
- `internal/handlers/chat/claude_cloaking.go` — Injected official `x-anthropic-billing-header` into `system[0]`, deterministic account `metadata.user_id`, client tool name obfuscation with `_ide` suffix, and decoy tools (`CCDecoyTools`) preventing false HTTP 429 anti-abuse rate limits.
- `internal/proxy/executor/claude_decloak.go` — Streaming and non-streaming response decloaker restoring original tool names and translating decoy tool invocations into clean text blocks.
- `internal/proxy/executor/providers.go` — Added `sanitizeToolUseID` to rewrite foreign/Gemini tool IDs deterministically to Anthropic-compliant `toolu_<sha256>`.
- `internal/tokensaver/prompts.go` — Added `InjectSystemPromptClaude` for format-aware system prompt injection at top-level `system`.

**Antigravity Zen Free-Tier Tool Quartet Renaming (PR #12):**
- Contributed by **@yxxrn** ([#12](https://github.com/luqman-v1/9router-go/pull/12)) — Special thanks for identifying the exact upstream fingerprinting gate and eliminating Claude Code 403/500 errors!
- `internal/translator/fingerprint.go` — Implemented `ConcealFingerprintTools` to rename uppercase tool quartet variants from Claude Code CLI (`Bash`, `Glob`, `Grep`, `Read`) to canonical lowercase (`bash`, `glob`, `grep`, `read`), eliminate duplicates (preventing upstream HTTP 500), retarget `tool_choice`, and restore original tool names in response payloads via `RestoreToolNamesInPayload` / `RestoreToolNamesInSSE`.
- `internal/proxy/executor/toolname_writer.go` — Embedded `toolNameRestoringWriter` on responses ensuring client tools are seamlessly restored across both streaming and non-streaming responses.

### 🐛 Bug Fixes & Improvements

**CommandCode CLI User-Agent & Schema Wrapping (PR #14, fixes #9):**
- Reported by **@jhonoryza** ([#9](https://github.com/luqman-v1/9router-go/issues/9)) — Thank you for reporting the Cloudflare challenge error!
- `internal/providers/providers.go` & `internal/proxy/executor/providers.go` — Added official `User-Agent: commandcode/0.25.7 (cli)` and `x-command-code-version: 0.25.7`, eliminating Cloudflare WAF bot-challenge intercepts (HTTP 403 `Attention Required!`).
- `internal/proxy/executor/providers.go` — Implemented `buildCommandcodeBody` wrapping OpenAI payloads into `{threadId, memory, config, params}` schema required by CommandCode's `/alpha/generate` endpoint.
- `internal/handlers/chat/fallback.go` & `internal/proxy/proxy.go` — Enhanced error parsing in `extractErrorText` and `UpstreamError.Error()` to summarize Cloudflare challenge pages cleanly without dumping raw HTML.

**Responses API (`POST /v1/responses`) Public Provider Fallback & String Input (PR #15, fixes #10):**
- Reported by **@pankaj-raikar** ([#10](https://github.com/luqman-v1/9router-go/issues/10)) — Thank you for the detailed reproduction report!
- `internal/handlers/media/media.go` — Added automatic fallback for public/free-tier providers (`DefaultAPIKey: "public"`) in `forwardMediaRequest`, eliminating `"no active connections for provider: opencode"` when no SQLite connection is seeded.
- `internal/handlers/media/media.go` & `internal/handlers/chat/resolution.go` — Routed `opencode`, `opencode-go`, and `antigravity/muse-spark-*` models on `/responses` directly to `ForwardOpencode` so session tracking, request headers, and tool-name concealing work out of the box.
- `internal/proxy/executor/transform.go` — Updated `buildResponsesBody` to support both string inputs (`"input": "Say hello"`) and array inputs (`Input []any`), normalizing string prompts into valid Responses message items.

## [v1.8.16] — 2026-09-17

### 🐛 Upstream Parity & Bug Fixes

**Active Provider Connections & Capabilities on `/v1/models` and `/api/models` (PR #8, fixes #7):**
- `internal/providers/registry_models.go` — Added comprehensive upstream provider models registry mapped from 128 provider definitions (`decolua/9router` parity) with `GetProviderModels()`.
- `internal/providers/capabilities.go` — Implemented `CapabilitiesDetail` and `GetCapabilitiesDetailForModel()`, outputting full multimodal flags (`vision`, `pdf`, `audioInput`, `videoInput`, `imageOutput`, `audioOutput`), `thinkingCanDisable`, and dual token limits (`contextWindows` and `contextWindow`).
- `internal/handlers/chat/chat.go` — Replaced empty `/v1/models` responses by dynamically aggregating models for active connections (`isActive = 1`), honoring custom prefixes and connection-enabled models. Custom models from deactivated connections (`disabledProviders`) are automatically excluded, and clean static catalogs are returned when no database connections are configured.
- `internal/handlers/router.go` — Mounted `GET /api/models` and `GET /api/models/*` serving both `"data"` and `"models"` top-level keys for universal compatibility across OpenAI-compliant IDE clients and Next.js web dashboards.
- `internal/handlers/chat/models_provider_test.go` — Added regression tests verifying model discovery for active Codex (`cx`) connections, fallback registry, and single-model lookups.

## [v1.8.15] — 2026-09-17

### 🚀 Features & Upstream Parity

**Claude Messages to OpenAI Response Translation for Zen / Union-Alpha (PR #4099, #4111):**
- `internal/translator/claude_response.go` — Added on-the-fly streaming (`TranslateClaudeChunkToOpenAI`) and non-streaming (`TranslateClaudeResponseToOpenAI`) response translation engines. Transforms Claude Messages SSE events (`content_block_delta`, `thinking_delta`, `tool_use`, `input_json_delta`, `message_delta`, `message_stop`) into standard OpenAI chunks (`choices[0].delta.content`, `reasoning_content`, `tool_calls`) so that client harnesses (e.g. omp, Cursor, Cline) receive native responses.
- `internal/proxy/executor/claude_messages.go` — Added dedicated streaming handler (`handleClaudeMessagesStream`) and non-streaming handler (`handleClaudeMessagesNonStream`) wired into `ForwardOpencode` and `ForwardOpencodeGo` for `union-alpha` routes.
- `internal/proxy/opencode.go` — Updated Antigravity Zen headers to comply with upstream PR #4111 (`User-Agent: antigravity/1.18.31 ai-sdk/provider-utils/4.0.46 runtime/bun/1.3.14`, `x-antigravity-client: cli`, dynamic 40-character hex project IDs `GenerateOpenCodeProjectID()`, and `x-api-key: public`).

**Anthropic Tools & Messages Schema Normalization:**
- `internal/proxy/executor/providers.go` — Added `convertOpenAIToolsToClaude`, `ensureMessagesMaxTokens`, `extractClaudeSystemPrompt`, and `convertOpenAIMessagesToClaude` to convert incoming OpenAI tool definitions (`type: "function"`) into Claude tools (`{name, description, input_schema}`), normalize `tool_choice`, and merge adjacent same-role messages for compliant Anthropic payload delivery.
- `internal/proxy/sse.go` — Expanded terminal detection buffer to 64 bytes and added recognition for Anthropic terminal signals (`"stop_reason":` non-null and `"message_stop"`) to eliminate premature `finish_reason: "network_error"` synthesis at clean stream EOF.

### 🐛 Bug Fixes & Resilience

**Google RPC `quotaResetDelay` Automatic Duration Locking with Deadlock Prevention:**
- `internal/handlers/chat/fallback.go` — Implemented `extractResetDuration` to parse Google RPC ErrorInfo metadata `quotaResetDelay` (e.g. `"1h12m28.109534319s"`) and text patterns (`"Resets in XhYmZs."`). Enforces safety bounds (min 5s, hard cap at 2 hours) to avoid perpetual lockouts or deadlocks.
- `internal/handlers/chat/combo.go` — Updated `comboLockRetryable` to use the parsed reset duration for connection and model locks instead of falling back to 8s exponential backoff.
- `internal/handlers/chat/antigravity_quota.go` & `internal/handlers/chat/gemini_handler.go` — Added `BlockAntigravityModelUntil` to cache exhausted model quotas and canonical synonyms (`gemini-3.8-flash-tiered`) in RAM until the verified reset timestamp, preventing continuous 429 spam to Google upstream while automatically unblocking the moment reset time is reached.

## [v1.8.14] — 2026-09-17

### 🐛 Upstream Parity & Bug Fixes

**Muse Spark 1.3 FreeTier Authorization & Session Normalization (PR #4105, #4061, #4062):**
- `internal/proxy/antigravity.go` — Updated Antigravity OpenCode user-agent default to `antigravity/1.18.31` and implemented descending canonical 30-character session (`ses_` + 12 hex + 14 Base62) and message (`msg_` + 12 hex + 14 Base62) ID generators. This resolves HTTP 403 `FreeTierError` when routing requests to `oc/muse-spark-1.3-contributor-free`.
- `internal/proxy/executor/providers.go` — Added `normalizeMuseSparkResponsesBody` to strip prior multi-turn reasoning content and encrypted content blocks that trigger HTTP 400 parameter errors, while explicitly normalizing `tool_choice` to `"auto"` for Muse Spark 1.3.

**Missing `tool_call_id` FIFO Repair (PR #4090):**
- `internal/handlers/chat/tool_repair.go` & `internal/translator/request.go` — Added automatic repairing for client requests where `role: "tool"` or `function_call_output` messages omit `tool_call_id`. Uses FIFO pairing with un-paired assistant tool calls or mints deterministic call IDs to prevent strict upstreams (OpenAI, DeepSeek, Antigravity) from failing with HTTP 400.
- `internal/handlers/chat/chat.go`, `internal/handlers/chat/combo.go`, `internal/handlers/chat/fallback.go`, & `internal/proxy/executor/transform.go` — Integrated tool call repair across OpenAI chat completions, combo routing, and account fallback handlers.

**Stream Interruption Terminal Synthesis (PR #4079):**
- `internal/proxy/sse.go` — Implemented terminal frame synthesis (`finish_reason: "network_error"` followed by `data: [DONE]\n\n`) when upstream SSE connections terminate abruptly at EOF before emitting a terminal frame, preventing IDE client hangs and errors in Cline/Pi.

**Claude Tool Result Image Hoisting (PR #4083):**
- `internal/translator/request.go` — Converted base64 image blocks embedded inside Claude `tool_result` into follow-up user messages with `[Image from tool result <id>]` and OpenAI `image_url` blocks, allowing vision models to inspect tool screenshot outputs without violating text-only tool-role schema constraints.

**Grok CLI Tool Result Neutral Placeholder (PR #4109):**
- `internal/proxy/grokcli.go` & `internal/mitm/handlers/kiro.go` — Replaced placeholder `"continue"` with neutral `"Tool results provided."` for tool-result user turns in Kiro request payloads to prevent assistant hallucination loops.

**Thinking Variant Route Stripping & Union Alpha Messages Route (PR #4084, #4099):**
- `internal/proxy/executor/providers.go` — Stripped model suffix before checking `isOpencodeResponsesModel`, enabling models like `gpt-5.6-luna(high)` to correctly route to `/responses`.
- `internal/proxy/executor/providers.go` — Routed Antigravity Zen `union-alpha` directly to `/zen/v1/messages` with `anthropic-version: 2023-06-01`.
- `internal/providers/capabilities.go` — Registered model capabilities for `union-alpha`, `deepseek-v4.1-flash`, and `deepseek-flash`.

## [v1.8.13] — 2026-09-16

### 🐛 Bug Fixes & Parity

**Grok CLI Responses Endpoint Fix (PR #4, fixes #2):**
- `internal/providers/providers.go` — Updated `grok-cli` `BaseURL` from bare root `https://cli-chat-proxy.grok.com` to `https://cli-chat-proxy.grok.com/v1/responses`, resolving HTTP 404 HTML edge errors when calling `gcli/*` models (`grok-4.5`, `grok-4.6`).
- `internal/proxy/grokcli.go` — Added auto-normalization in `ForwardGrokCLI` so that if `cfg.BaseURL` is empty or lacks the `/v1/responses` path, it automatically normalizes to `/v1/responses`.
- `internal/handlers/chat/grokcli_handler_test.go` — Added regression tests for grok-cli BaseURL and endpoint routing.

**Custom Provider Nodes Prefix Priority & Model Mapping (PR #5, fixes #3):**
- `internal/handlers/chat/resolution.go` — Prioritized custom `providerNode` prefix resolution (`h.resolvePrefixProvider(prefix, model)`) before checking built-in provider aliases (`resolveProviderAlias(prefix)`). This prevents short prefixes like `oa` or `cc` from being shadowed by `openai` or `claude`, eliminating false 502 "no active connections for provider: openai" failures when the custom node has active connections.
- `internal/handlers/chat/resolution.go` — Added fallback so that when the alias-resolved provider has no active connections, unresolved prefix queries report the matching custom `providerNode.id` instead of falsely blaming the shadowed built-in provider.
- `internal/handlers/chat/resolution.go` — Added defensive nil checks for `h.Repo` across all model and combo resolution helpers.
- `internal/db/repos.go` — Added `GetProviderNodePrefixMap()` to map internal row IDs (`openai-compatible-chat-0489...`) to user-configured prefixes (e.g. `nara`, `orca`, `oa`).
- `internal/db/repos.go` — Updated `GetCustomModels()` with fallback parsing from keys (`<providerAlias>|<modelId>|<kind>`) and removed restrictive `type == "llm"` filtering, exposing all custom chat and completion models.
- `internal/handlers/chat/chat.go` — In `HandleModels` (`GET /v1/models`) and `HandleModelLookup` (`GET /v1/models/*`), mapped `cm.ProviderAlias` through the prefix map so models are published under their clean user-configured prefix (e.g. `nara/glm-5.3`) with `owned_by` set to the prefix rather than leaking internal database row IDs.
- `internal/handlers/chat/resolution_test.go`, `internal/handlers/chat/chat_v065_test.go`, & `internal/db/repos_test.go` — Added comprehensive unit and regression tests for custom prefix priority, fallback error reporting, prefix map caching, key-fallback parsing, and `/v1/models` prefix output.

## [v1.8.12] — 2026-09-16

### 🚀 Features & Provider Additions

**Freebuff Provider Integration (`fb`):**
- `internal/proxy/executor/freebuff.go` — Added native Freebuff executor supporting `https://www.codebuff.com/api/v1/chat/completions` with 1-hour session token lifecycle caching, agent run tracking (`/api/v1/agent-runs`), Buffy system prompt marker injection, and `end_turn` tool injection for sub-agent orchestration.
- `internal/providers/providers.go` & `internal/providers/aliases.go` — Registered provider `freebuff` and alias `fb`.

**Provider Connection Routing Strategies:**
- `internal/db/settings.go` & `internal/handlers/chat/connections.go` — Added configurable multi-connection routing strategies per provider: `sticky` (with configurable `stickyLimit`), `round-robin`, `random`, and `none`.

**Model Capabilities & Limits:**
- `internal/providers/capabilities.go` — Added capabilities for Upstage Solar Pro (`*solar-pro*`) and LongCat (`*longcat*`) with reasoning, tools, 200,000 token context window, and 32,000 max output tokens.

### 🐛 Bug Fixes & Resiliency

**Cline & Clinepass OAuth Refresh Overhaul:**
- `internal/proxy/oauth/cline.go` — Registered `clinepass` alongside `cline` in the OAuth registry, resolving issues where ClinePass connections fell back to incompatible standard form-urlencoded OAuth refresh.
- Migrated token refresh endpoint from deprecated `/v1/auth/refresh` (which returned 401 "Please make sure you're using the latest version of Cline") to active upstream `/api/v1/auth/refresh`.
- Emulated full Cline CLI identity headers on refresh (`User-Agent: Cline/3.0.61`, `X-CLIENT-TYPE: cline-cli`, `X-CLIENT-VERSION: 3.0.61`, `X-CORE-VERSION: 3.0.61`, `X-PLATFORM: cli`).
- Added token rotation support: propagated rotated `refreshToken` to SQLite database across `BuildConnectionUpdate` and `forceRefreshOAuthToken`.
- `internal/handlers/chat/fallback.go` — Ensured `refreshedKey` is normalized with `NormalizeProviderToken` on reactive 401 retries so WorkOS prefix (`workos:`) is preserved.

**Zero-Sleep Failover & Canonical Model Locking:**
- `internal/handlers/chat/combo.go` — Removed synchronous blocking sleeps (`time.Sleep`) during combo failover loops on transient errors (502, 503, 504), enabling immediate non-blocking failover to backup models/connections without stalling client turns.
- `internal/handlers/chat/connections.go` & `internal/handlers/chat/combo.go` — Added `canonicalLockModel(provider, model)` to atomically lock shared tier pools across connections (e.g., Antigravity `gemini-3.8-flash-low`/`high` map to canonical lock key `gemini-3.8-flash-tiered`). Prevents split-lock failure loops across shared tier accounts.
- `internal/providers/errorclassify.go` — Expanded error classification to trigger backoff for `resource_exhausted`, `model_capacity_exhausted`, and HTTP 502/503/504 status codes.

**Stream Telemetry & Connection Safety:**
- `internal/proxy/sse.go` — Added rolling 16-byte tail buffer in `SSECopy` to safely detect `[DONE]` across chunk boundaries and cleanly terminate SSE streams without hanging on keep-alive connections.
- `internal/handlers/chat/fallback.go` — Guaranteed `usageHistory` database logging on completed streams even when the client disconnects at stream end.
- Advertised `context_window` in `/v1/models` and `/v1/models/info` dynamically from capabilities.

## [v1.8.11] — 2026-09-12

### 🐛 Bug Fixes & Parity — Upstream PR Porting

**Gemini Multiple System Messages Preservation (PR #3973):**
- `internal/translator/gemini.go` — Preserved all `role: "system"` messages in `req.SystemInstruction.Parts` rather than overwriting earlier instructions with the last turn, ensuring all system prompts and developer directives reach Gemini models.

**Antigravity Thinking Budget & Output Tokens Guard (PR #3981):**
- `internal/translator/gemini.go` & `internal/translator/antigravity.go` — Guarded `maxOutputTokens > thinkingBudget` across `TranslateOpenAIToGemini` and `hardenAntigravityRequest`, preventing HTTP 400 `INVALID_ARGUMENT: max_tokens must be greater than thinking.budget_tokens` and erroneous connection locks on reasoning models.
- Added support for `max_completion_tokens`, `thinking.budget_tokens`, and `thinking_budget`.

**Claude Document Block Support for Antigravity & OpenAI (PR #3968):**
- `internal/translator/request.go` & `internal/translator/types.go` — Added `document` block handling in `TranslateClaudeToOpenAI` and `OpenAIFile` struct in `OpenAIContentBlock`, converting base64 PDF documents into OpenAI file format that flows into Gemini/Antigravity `inlineData`.

**Client Cancellation Tracing & Stream Telemetry:**
- `internal/handlers/chat/fallback.go` — Differentiated client cancellations (`errors.Is(fwdErr, context.Canceled)` or `ctx.Err() != nil`) from true upstream failures, logging `INF [fallback] client canceled request` and recording status `499` in traces instead of raising false `WRN upstream failed` alarms.
- `internal/handlers/chat/gemini_handler.go` — Added accurate `totalBytesWritten` accumulation for OpenAI format streaming branches and terminal `[DONE]` frame.
- `internal/handlers/chat/fallback.go` & `internal/handlers/chat/combo.go` — Refactored hardcoded HTTP status codes to standard `net/http` constants (`http.StatusOK`, `StatusClientClosedRequest`).

**Memory Leak Protections & High-Traffic Concurrency:**
- `internal/translator/usage.go` & `internal/translator/response.go` — Added `pendingFragment` struct with `createdAt` timestamps and 10-minute TTL pruning in `pruneStaleStatesLocked()`. Prevents abandoned fragmented SSE streams from accumulating in the global `pendingJSON` map.
- `internal/handlers/chat/connections.go` — Pooled and cached `*http.Client` and `*http.Transport` instances by proxy URL using `sync.RWMutex`. Eliminates per-request transport allocations, enables TCP keep-alive reuse across proxy pool traffic, and prevents socket/goroutine exhaustion.

**Live Profiling & Diagnostics:**
- `internal/handlers/router.go` — Mounted Go standard `net/http/pprof` endpoints (`/debug/pprof/`, `/debug/pprof/heap`, `/debug/pprof/goroutine`, `/debug/pprof/profile`) for real-time heap and concurrency inspection.

**Documentation & Client Guides:**
- `README.md` — Added comprehensive pre-built binary download links (macOS, Linux, Windows), one-liner install script, Docker setup, and configuration examples for Claude Code, `omp`, and Cursor/Cline.

## [v1.8.10] — 2026-09-11

### ✨ Features & Parity — Next.js v0.5.75 Sync (27 Commits)

**Gemini & Antigravity Content Normalization:**
- `internal/translator/gemini.go` & `internal/translator/antigravity.go` — Added `NormalizeGeminiContents` merging adjacent same-role messages and filtering out empty parts (parity with `#e7b5f09`).
- `internal/handlers/chat/antigravity_quota.go` — Added Antigravity weekly quota tracking (`gemini_weekly`, `claude_gpt_weekly`) and free-tier handling via `retrieveUserQuotaSummary`, caching summaries and reconciling against exhausted model families (#3892).

**Kiro Routing & Wire Payload Cleanup:**
- `internal/providers/providers.go` & `internal/proxy/grokcli.go` — Routed Kiro through Amazon Q first (`https://q.us-east-1.amazonaws.com/generateAssistantResponse`), deprecated legacy runtime path to avoid 400 `REQUEST_BODY_INVALID` (#3776).
- Injected `x-amz-sso-bearer`, `x-amzn-kiro-agent-mode: spec`, and `x-amzn-codewhisperer-machine-id: kiro-desktop` headers.
- `internal/proxy/grokcli.go` & `internal/mitm/handlers/kiro.go` — Stripped top-level `systemPrompt`, `agentMode`, and `conversationState` continuation fields (`agentContinuationId`, `agentTaskType`) that modern Kiro gateways reject with 400 (#1892ed7).

**Opencode-Go Catalog Refresh & Responses API:**
- `internal/proxy/executor/providers.go` — Routed `grok-4.6` and `gpt-5.6-luna` on `opencode-go` to the `/zen/go/v1/responses` endpoint alongside `muse-spark`.
- `internal/providers/capabilities.go` & `internal/proxy/executor/providers.go` — Registered newly published Go models (`deepseek-flash`, `glm-5.3`, `kimi-k3`, `longcat-2.0`, `qwen3.8-max`, `qwen3.8-flash`, `hy4-preview`, `hy3`), with `deepseek-flash` (DeepSeek V4.1 Flash) priority and Qwen 3.8 models in `opencodeGoMessagesModels`.

**Codex CLI Bump & Unicode Schema Sanitization:**
- `internal/providers/providers.go` — Updated Codex CLI User-Agent to `codex_cli_rs/0.154.0` (parity with `#a7047a0`).
- `internal/proxy/executor/transform.go` — Added `StripCodexUnsupportedPatterns` to sanitize `\p{...}` / `\P{...}` Unicode property escapes in tool parameters that Codex's `/responses` validator rejects with HTTP 400 (#3922).
- `internal/providers/capabilities.go` — Added Codex image models (`gpt-image-2.5`, `gpt-image-2.5-flare`, `gpt-image-2.5-sunburst`, `gpt-image-2`, `gpt-image-1.5`) with `ImageOutput` capability and `*gpt-image*` pattern match.

**Claude Cache Budget & Single-Object Turns:**
- `internal/translator/request.go` — Enforced Anthropic 4-marker `cache_control` budget in `AnchorClaudeCache`: pins head anchors (last system block, last non-deferred tool) and keeps at most 2 tail message markers, trimming earlier ones (#8a81085).
- `internal/translator/request.go` — Supported single-object content turns (`content: {type: "text", ...}`) across `convertClaudeMessage`, `SanitizeClaudePassthrough`, and `AnchorClaudeCache`.
- `internal/handlers/chat/chat.go` — Scoped Claude tool type defaulting to gateways declaring `requireClaudeToolType` (MiniMax / MiniMax-CN), avoiding 400 `unknown variant custom` on DeepSeek Anthropic endpoint (#3905, #45ec1d3).

**Cline Envelope Unwrapping & Token Refresh:**
- `internal/translator/response.go`, `internal/handlers/chat/forward.go`, `internal/proxy/executor/openai.go` — Added `UnwrapClineEnvelope` unwrapping `{"success":true,"data":{...}}` for non-streaming completions on `cline` and `clinepass` (#122f23ee).
- `internal/providers/oauth.go` — Registered `clinepass` in OAuth token refresh config.
- `internal/providers/capabilities.go` — Replaced `deepseek-v4-flash` with `deepseek-v4.1-flash` for `codebuddy-cn` (#807553e).

**Connection Health & Security:**
- `internal/db/accounts.go` — Added `ResetConnectionHealthState` clearing `modelLock_*`, `errorCode`, `rateLimitedUntil`, and resetting `backoffLevel = 0` upon connection activation (#3830).
- `internal/handlers/media/media.go` — Rejected path-escaping characters (`..`, `/`, `\`) in `HandleVideoGet` (#da6aa901).

**Responses API Stream & Tool Calling Fixes:**
- `internal/proxy/executor/stream.go` — Fixed tool call argument duplication (`InputValidationError` caused by duplicate concatenated JSON bodies such as `{"q":"*.yaml"}{"q":"*.yaml"}`) on Responses API streams by tracking `ArgsEmitted` during `response.function_call_arguments.delta` and suppressing redundant full argument re-emission on `response.output_item.done`.
- `internal/handlers/chat/gemini_handler.go` — Ensured terminal `data: [DONE]\n\n` SSE frame is emitted upon stream completion for OpenAI-format streaming clients and cleaned up redundant newlines.
- `internal/handlers/chat/live_e2e_test.go` — Added live non-mock E2E tests for Antigravity (`gemini-2.5-flash`, `gemini-3.8-flash-high`), DeepSeek, and OpenCode covering parallel multi-tool calls, multi-turn tool execution, streaming, and weekly quota retrieval.

## [v1.8.9] — 2026-09-06

### ✨ Features & Parity — Next.js v0.5.69 Sync (19 Commits)

**Gemini & Antigravity ThoughtSignatureStore:**
- `internal/translator/thought_signature_store.go` — Added thread-safe in-memory LRU store (capacity: 2,000 entries, 1-hour memory TTL) scoped by `sessionId` + `toolCallId`. Caches and replays thought signatures across turns to prevent corrupted thought signature errors during multi-turn reasoning conversations.
- `internal/translator/gemini.go` & `antigravity.go` — On parallel function calls, only the first call receives the signature/fallback, leaving sibling calls unsigned per Google Gemini 3+ specification.

**New Models & Capabilities Sync:**
- `internal/providers/capabilities.go` — Registered **`gpt-6-astra`** (Vision, Reasoning, Search, Tools; 272K window / 128K max output) and added `*gpt-6*` pattern match.
- Registered GPT-5.6 image aliases: `gpt-5.6-sol-image`, `gpt-5.6-terra-image`, and `gpt-5.6-luna-image` with `ImageOutput` capability.
- Qoder capabilities catalog refresh (`ultimate`, `performance`, `gmodel`, `gfmodel`, `qmodel_38max`, etc.).
- CodeBuddy-CN capabilities updated (`glm-5.2` vision enabled).

**Anti-Abuse Google Token Refresh (Antigravity):**
- `internal/handlers/chat/antigravity_project.go` — Configurable `ONBOARD_MAX_ATTEMPTS` (default 2, down from 5) and `ONBOARD_RETRY_DELAY_MS` (default 12s) to prevent Google account rate-limit blocks during multi-account refresh (#3813).

**Anthropic-Beta Header Forwarding & Effort Normalization:**
- `internal/handlers/chat/fallback.go` — Automatically injects `Anthropic-Beta: prompt-caching-scope-2026-01-05, context-management-2025-06-27` for `anthropic-compatible-*` nodes serving Claude models (#3797).
- `internal/translator/request.go` & `types.go` — Normalizes Claude adaptive auto effort (`output_config.effort="auto"` and `"xhigh"` $\to$ `"high"`) (#3792).

**OpenCode-Go Executor & Responses Parallel Tool Calls Fixes:**
- `internal/proxy/executor/providers.go` — Added `deriveOpencodeSession` generating stable `x-opencode-session: ses_<32hex>` headers for all `opencode-go` requests, with fallback and client tool isolation (#3800).
- Routed `muse-spark-1.2-contributor` and `muse-spark-1.3-contributor` on `opencode-go` to the `/responses` endpoint (#3819, #3820).
- `internal/proxy/executor/stream.go` — Fixed parallel tool calls argument collision on Responses API SSE stream by indexing events via `item_id`. Emits arguments from `response.output_item.done` when upstreams send arguments on item completion without deltas.
- `internal/proxy/executor/stream.go` — Fixed `handleCodexStream` SSE stream truncation and disconnects on `muse-spark-1.3` (and 1.2) by switching to `proxy.ScanStream` (up to 10MB buffered scanner), preventing line fragmentation when handling large (>3KB) encrypted reasoning payloads across TCP packet boundaries.
- `internal/proxy/executor/stream.go` — Added support for `response.reasoning_summary_text.delta`, `response.reasoning_text.delta`, and `response.thought.delta` emitting `reasoning_content` delta chunks for thinking models.
- `internal/proxy/sse.go` — Added `HeartbeatWriter` emitting periodic `: keep-alive\n\n` comments every 15 seconds during prolonged upstream reasoning phases (fixes #3796 stream stall timeouts on strict clients like Oh My Pi during deep thinking on `ag/gemini-3.8-flash*`).
- `internal/proxy/stall.go` — Added `NewStallReaderWithContext` binding client `ctx.Done()` directly to body closer, immediately freeing upstream sockets on client abort and eliminating Windows socket leaks (`CLOSE_WAIT`/`FIN_WAIT_1`).
**Database Path Configuration:**
- `internal/config/config.go` — Enhanced `DB_PATH` resolution to automatically detect `db/data.sqlite`, `data.sqlite`, or `9router.db` when pointed directly to a directory (e.g. `E:\project\database\9router`).
## [v1.8.8] — 2026-09-03

### ✨ E2E & Parity — Next.js v0.5.65 (31 commits)

**E2E Gemini 3.8 Flash High + tool calling (deterministic mocks):**
- `internal/handlers/chat/gemini38_e2e_test.go` — `gemini-3.8-flash-high` via Antigravity `2.11.0` non-stream + multi-turn + stream SSE `get_weather_ide` uncloaking, `prefixItems` cleaning, `thoughtSignature` backfill, `tool_calls` dedup. Ports `decolua/9router` `gemini-3.8-flash-medium/high/low` + `capabilities.go:*gemini-3.8*` + `antigravity.go:gemini-3.8-flash-tiered` + `proxy/gemini.go:2.11.0`.

**E2E Opencode muse-spark (deterministic mocks, no real network):**
- `internal/handlers/chat/opencode_mock_e2e_test.go` — `oc/muse-spark-1.2` & `1.3` via `Responses API /v1/responses` SSE `output_item.added` + `function_call_arguments.delta/done` aggregation, `reasoning max→xhigh`, `Vision:true` (`capabilities.go:124` pattern `*muse-spark*`), `image_url` preservation. Fixes routing `muse-spark-1.3` `500` → `200` (`providers.go:293` `Contains(muse-spark)` + `capabilities.go:124` `1.3`).

**Unit tests — now locking logic (previously untested):**
- `providers_v065_test.go` — `claude-cli/2.1.258` + full `Anthropic-Beta`, `ollama FetchURL https://ollama.com/api/web_fetch`, `gemini-3.8` caps, `muse-spark 1.2/1.3`, `codebuddy-cn hy3/hy3-x/hy4-preview/x/glm-5.3/kimi-k3-1` + EOL `glm-5.0/4.7` removed, `GetModelTokenLimits` 3.8.
- `translator/claude_cache_test.go` — `LastCacheableToolIndex` + `AnchorClaudeCache` for `defer_loading:true` tail, all-deferred, stripping client `cache_control` (#3567).
- `handlerutil/ssrf_test.go` — `trailing dot` (`localhost.`), `CGNAT 100.64/10`, `169.254.169.254`, IPv6 `::ffff:7f00:1` hex, `64:ff9b::`, `fe80/fc`, `normalizeHost`, `parseIPv6ToGroups`.
- `mitm/handlers/mitm_handlers_test.go` — `HandleKiro` removes `systemPrompt` + `userInputMessage.images → image_url data:`, `HandleAntigravity` preserves `fetchAvailableModels(2.11.0)` vs overrides `generateContent→1.23.2`.
- `usagetracker/quota_parsers_test.go` `TestParseGroqQuotasFromHeaders` — `x-ratelimit-*` Go duration `2m59.56s` → `requests/tokens` `used/total/resetAt`.
- `handlers/chat/chat_v065_test.go` — `HandleModelLookup` kind `image` + `cc/claude-sonnet-4-6` + encoded slash + 404 `model_not_found`, `HandleModels` custom `cc/my-custom-vision` caps, `StrikeReassert` 3×429 optimistic 90% → `CACHE_BLOCK 15m` + re-assert after `Refresh`.
- `proxy/executor/opencode_test.go` `MuseSpark13_ResponsesRouting` + `OCPrefix` — routing `1.3` + `oc/` to `/responses`.

**Fixes:**
- **Opencode 1.3 `500` → `200`** — `ForwardOpencode` routing `Contains(muse-spark)` + `capabilities` `1.3` Vision (fixes report `14:11:47` `muse-spark-1.3 500`).
- **jcode tool_smoke 3→1** — `stream.go:118` dedup `ToolCallIdx` + `codebuddy.go:168` `sseToOpenAIJSON` dedup `arguments` for `bash` `intent` split (fixes `echo JCODE_TOOL_OK` 3 tool_calls).
- **Flaky real upstream 429** — `muse_spark_e2e_test.go` real `opencode.ai` `429 FreeUsageLimitError` now `Skip` instead of `Fail`.
- **DB flaky `429` in `go test ./...`** — `go vet` clean, `ps` `9router-go 20130` health `{"status":"ok"}` (not stopped, log stopped due to `user stepped away` recap 98k prompt).

## [v1.8.7] — 2026-09-02

### 🐛 Bug Fixes

- **Gemini `system_instruction` Empty Part 400 Fix** — `StripCompetitivePrompts` now drops empty `system_instruction` parts after `rewriteCompetingBranding` (e.g. `"You are a Claude agent..."` -> `""`) and filters empty text parts in `contents`; if all parts are empty the `system_instruction` is removed (`nil`) instead of emitting `{"parts":[{}]}` which Gemini rejects as `system_instruction.parts[0].data: required oneof field 'data' must have one initialized field`. Also `TranslateOpenAIToGemini` now `TrimSpace` checks system content. Fixes `ForwardGemini (antigravity/gemini-3.7-flash-high): ...system_instruction.parts[0].data: required oneof`. (`internal/translator/antigravity.go`, `internal/translator/gemini.go`)
- **Gemini Tool Schema `required` Inside `properties` 400 Fix** — `cleanGeminiSchema` now detects misplaced `required` array inside `properties` (e.g. `{"properties":{"query":{...},"required":["query"]}}`) and promotes it to top-level `required`, fixing `Invalid value at 'request.tools[0].function_declarations[0].parameters.properties[0].value' (Map), Cannot have repeated items ('required') within a map. Unknown name ""`. This was the root cause of the `12:14:50` `query_db_ide` 400 after the `where.items` fix. Also sanitizes all OpenAI-compatible providers (including `opencode`) via `fallback.go` `SanitizeOpenAITools`. (`internal/translator/schema.go`, `internal/handlers/chat/fallback.go`)
- **Opencode `muse-spark` Tool Name Triplication Fix** — `sseToOpenAIJSON` `internal/proxy/executor/codebuddy.go:168` now only sets `name` if empty and avoids duplicating `arguments` already sent via `delta`/`done`; `ProcessCodexEvent` `stream.go:148` for `response.function_call_arguments.delta/done` now deduplicates `name` and tracks `ToolCallArgs` to prevent `get_weather` -> `get_weatherget_weatherget_weather` and `{"location":"Jakarta"}{"location":"Jakarta"}` on `combo-wombo` (`oc/muse-spark-1.2`) non-stream and stream. (`internal/proxy/executor/codebuddy.go`, `internal/proxy/executor/stream.go`)

## [v1.8.6] — 2026-09-02

### 🐛 Bug Fixes

- **Gemini Tool Schema `where.items.items: missing field` 400 Fix** — `cleanGeminiSchema` now ensures every `type: array` has a valid `items` schema (default `{"type":"string"}`), flattens `prefixItems` (2020-12 tuple) and `items: [...]` tuple to single `items`, and auto-fills inner `items` without `type`/`properties`/`enum`. Fixes `ForwardGemini (antigravity/gemini-3.7-flash-high): upstream returned 400: ...where.items.items: missing field.` when Claude Code sends DB-like tools with nested `array<array>` params. Added `internal/proxy/gemini.go` 400 payload dump to `/tmp/9router-gemini-400.json` for post-mortem. (`internal/translator/schema.go`, `internal/proxy/gemini.go`)
- **Bare Alias `ag` -> `antigravity` Resolution** — `resolveModel("ag")` now checks `ProviderAliasMap` before common-provider fallback, so `POST /search` with `{"model":"ag"}` correctly routes to `antigravity` instead of `deepseek/ag` -> `404 Via cloudfront`. Parity with Next.js `ag` search. (`internal/handlers/chat/resolution.go`)
- **Antigravity Search Default Model Parity** — `handleAntigravitySearch` default changed `gemini-3-flash-agent` -> `gemini-2.5-flash` (Next.js `ag` search returns `answer.model: gemini-2.5-flash`), fixing `500 UNKNOWN` from `daily-cloudcode-pa.googleapis.com` for bare `ag` search. (`internal/handlers/media/antigravity_search.go`)
- **Claude `call_` Tool Name Fallback Fix (Gateway IP Check)** — `TranslateOpenAIToClaude` no longer falls back to `tc.ID` (`call_...`) when `Function.Name` is empty; skips invalid tool calls instead of emitting `tool_use` with `name: call_...` which caused `No such tool available: call_...` and `Invalid tool parameters` churn on `toloing cek config gateway` via `opencode/muse-spark`. (`internal/translator/response.go:165`, `cf42120`, port `decolua/9router#2077`/`#3685`)
- **Claude Streaming Tool Delta Deduplication** — `TranslateOpenAIToClaudeStreamSession` now checks `state.ToolCalls[idx]` before creating a new `content_block_start`; second delta for same `idx`/`id` (Codex `output_item.added` + `function_call_arguments.delta` same `call_...`) no longer creates duplicate `tool_use` and empty `partial_json: "{}"`; correctly buffers `arguments` and emits `{"command":"ip route ..."}`. Fixes `InputValidationError: Bash missing command` on `Gue cek gateway IP...` streaming. (`internal/translator/response.go:468`, `ea10c16`)
- **Bash Extra Fields Strip** — `sanitizeBashArgs` now keeps only `command`, deletes hallucinated `description` etc. inside `input` (`{"command":"ls ...","description":"List home..."}` -> `{"command":"ls ..."}`), fixing `Bash(input JSON failed to parse — 433 bytes)` on `Cek gateway config — lagi intip file-file di home`. (`internal/translator/sanitize.go:239`, `6b94535`)
- **Server Tool Use Foreign ID Drop (Combo Poison)** — `SanitizeClaudePassthrough` drops `server_tool_use` with id not matching `^srvtoolu_` (e.g. `call_` from `z.ai/glm` `analyze_image`) and paired `tool_result`, strips empty text and empty messages. Port `decolua/9router#3686`. (`internal/translator/request.go:342`, `cf42120`)
- **1M Context Marker Strip** — `stripModelContextMarker` strips trailing `[1m]`/`[1M]` from `claude-opus-5[1m]` before `resolveModel`, so combo `combo-wombo[1m]` routes correctly instead of `Invalid model format`. Port `decolua/9router#3691`. (`internal/handlers/chat/resolution.go:135`)
- **Streaming Model Echo** — `SeedStreamState` pre-seeds `StreamState.Model` with client-requested model via `WithRequestedModel` context so `message_start` echoes `combo-wombo` not `claude-3-5-sonnet` provider model. Port `decolua/9router#3693`. (`internal/translator/usage.go:41`, `forward.go:91`)
- **GPT-5 / o-series `max_completion_tokens`** — `requiresMaxCompletionTokens` (`/gpt-5|o[134]-/i`) emits `max_completion_tokens` instead of `max_tokens` for `gpt-5`/`o1-`/`o3-`/`o4-` in `TranslateClaudeToOpenAI`. Port `decolua/9router#3657`. (`internal/translator/request.go:12`, `types.go:202`)
- **Antigravity Optimistic Quota Strike-Breaker** — after 3 consecutive `429` for same `connection|model` within 60s while quota `remaining>0`, `HandleAntigravityQuotaError` returns `CACHE_BLOCK 15m` instead of looping 300s `modelLock`. Port `decolua/9router#3684`. (`internal/handlers/chat/antigravity_quota.go:44`)
- **Forced-SSE JSON for Claude Clients** — `handleJSONResponse` detects `isSSEBody` when `translate=true` and `stream:false` retry hits forced-stream provider (Responses-API), aggregates via `sseToClaudeJSON` then `TranslateOpenAIToClaude` to return `Anthropic Message` not `chat.completion`. Port `decolua/9router#3683`. (`internal/handlers/chat/forward.go:144`)
- **OpenRouter Pattern Compat + Combo Tools Detection** — `NormalizeToolSchemasForProvider("openrouter")` strips invalid `pattern` regex (keep valid, handle `properties` named `properties`), and `DetectRequiredCapabilities` now requires `tools` capability for `type:function`/`functionDeclarations`. Port `decolua/9router#3665`. (`internal/translator/tool_schema.go`, `combo.go:129`, `forward.go:35`)

## [v1.8.5] — 2026-08-31

### ✨ Features & Parity (Next.js v0.5.59 Sync)

- **New Search Providers & Credential Fallback** — added `xquik` (X search provider with raw API key), `ollama-search`, and `zai-search` (GLM Coding web search). Added automatic credential fallback where search providers borrow API keys from parent chat connections (`ollama` / `glm`) when dedicated search connections are absent. (`internal/providers/providers.go`, `internal/providers/aliases.go`, `internal/handlers/chat/connections.go`)
- **Antigravity Web Search Provider** — added Antigravity as a web search provider via Google Search grounding, with full Next.js parity for the search response structure. (`internal/handlers/media/antigravity_search.go`)
- **New Models & Capabilities Sync** — registered new flagship models: `GLM-5.3-Flash` (1M context window + native vision multimodal), `GLM-5.3`, `DeepSeek V4 Vision`, `Grok 4.5/4.6` (500k context window), and `muse-spark-1.2-contributor-free`. (`internal/providers/capabilities.go`, `internal/providers/aliases.go`)
- **Claude Tool Type Defaulting (`type: "custom"`)** — added `DefaultClaudeToolType` ensuring tools in Claude-format requests always carry a valid `type` (defaulting to `"custom"` when omitted), preventing HTTP 400 rejection on strict Anthropic-compatible gateways such as MiniMax. (`internal/translator/request.go`, `internal/handlers/chat/chat.go`)
- **Claude Code Session ID Header Support** — prioritized `x-claude-code-session-id` in `ExtractSessionID` to ensure stable prompt caching and avoid conversation fragmentation across client tool calls. (`internal/handlerutil/response.go`)
- **CommandCode In-Stream Error Peeking** — peeks the initial NDJSON event in CommandCode stream for `type: "error"` before committing HTTP 200 OK headers, transforming internal stream errors into real HTTP error statuses (429, 503, 401, etc.) so combo and account fallback trigger seamlessly. (`internal/proxy/executor/stream.go`)
- **OpenCode Responses API Parity (v0.5.59)** — completed Responses API translation for OpenCode Muse Spark: proper tool names emitted on `response.output_item.added`, accurate usage and prompt-cache token extraction from `response.completed`, Claude SSE streaming translation and non-streaming support in `handleCodexStream`, and 64-char clamping for `call_id`. (`internal/proxy/executor/`)

### 🐛 Bug Fixes

- **Gemini Cached Token Extraction** — added support for both `cachedContentTokenCount` and `cachedContentToken` keys in Gemini stream and non-stream responses. (`internal/translator/gemini.go`)
- **Non-Interactive Test Execution** — bypassed interactive `sudo security` CA keychain install when executing unit tests, ensuring fast, deterministic test suite completion. (`internal/mitm/cert.go`)
- **Self-Update SHA256 Verification** — `PerformSelfUpdate` now downloads to memory, verifies the expected SHA-256 checksum, and refuses to install mismatched binaries, eliminating the risk of installing corrupted or tampered updates. (`internal/updater/updater.go`)
- **Graceful Self-Restart** — replaced abrupt `os.Exit` after self-update with `syscall.Kill(SIGTERM)` plus a graceful fallback, giving in-flight requests and DB connections a chance to drain cleanly. (`internal/updater/updater.go`)
- **Cross-Platform Restart** — extracted the self-signal into a platform-specific `signalSelfShutdown` helper (`signal_unix.go` sends SIGTERM; `signal_windows.go` is a no-op that falls back to `os.Exit(0)`), fixing the Windows cross-compile of the release binaries. (`internal/updater/signal_unix.go`, `internal/updater/signal_windows.go`)
- **Smart Archive Executable Selection** — `extractExecutableBytes` now scores archive entries (penalizing README/LICENSE/`*.md`/`*.sha256`) and validates ELF/Mach-O/PE magic bytes, reliably picking the real binary from multi-file release archives. (`internal/updater/updater.go`)
- **SSE Copy Race Condition** — replaced the shared pooled buffer in `SSECopy` with a per-call local buffer, eliminating concurrent read/write races on the pool buffer. (`internal/proxy/sse.go`)
- **Nil Guard in Token-Saving Compression** — guarded against a nil `rawMap` when the upstream body cannot be decoded, preventing a panic on malformed responses. (`internal/tokensaver/compress.go`)
- **Quota Percentage Clamping** — clamped `RemainingPercentage` to a sane `[0, 100]` range so upstream values >100 or negative cannot skew quota-block and dashboard logic. (`internal/usagetracker/quota_parsers.go`)
- **Exponential Backoff for Antigravity Onboarding** — replaced the fixed 2s sleep between `onboardUser` retries with exponential backoff (2s, 4s, ...) that also honors context cancellation, so a 429 burst no longer gets hammered by fixed-interval retries. (`internal/handlers/chat/antigravity_project.go`)
- **Decloak Deduplication** — extracted a shared `decloakContentBlockStart` helper used by both `DecloakStreamChunk` and `DecloakClaudeStreamEvent`, removing duplicate content-block-start logic. (`internal/translator/antigravity.go`)
- **Tool Property Sanitization** — preserved tool parameters named after reserved keywords and sanitized `required` fields against the declared `properties`, preventing schema validation failures. (`internal/translator/sanitize.go`)

## [v1.8.4] — 2026-08-14

### 🐛 Bug Fixes & Resilience

- **Combo Cycle Graceful Recovery & Fault Tolerance** — `flattenComboModels` now gracefully skips recursive / self-referencing combo branches with a warning log instead of failing hard with HTTP 400 (`combo cycle detected`), ensuring chatbot requests continue executing remaining valid models seamlessly. (`internal/handlers/chat/resolution.go`)
- **Safe Model Resolution on Leaf Models** — eliminates potential slice index-out-of-range edge cases when resolving combo leaf models that do not contain a provider prefix. (`internal/handlers/chat/resolution.go`)
- **Multi-Level Nested Combo Support** — verified recursive cascading combo expansion (e.g. `super-combo` → `mid-combo` → `base-combo` → leaf models) so all reachable models participate in round-robin, sticky, and fallback strategies. (`internal/handlers/chat/resolution.go`, `internal/handlers/chat/resolution_test.go`)

## [v1.8.3] — 2026-08-14

### ✨ Features

- **Antigravity Gemini 3.7 Flash Model Mapping** — canonical model IDs and aliases for `gemini-3.7-flash`, `gemini-3.7-flash-high`, `gemini-3.7-flash-agent`, `gemini-3.7-flash-medium`, `gemini-3.7-flash-low`, `gemini-3.7-flash-extra-low`, and `gemini-3.7-flash-thinking` correctly mapped to Google Antigravity backend model IDs (`gemini-3-flash-agent` / `gemini-3.5-flash-low`), fixing upstream 404 errors. (`internal/translator/antigravity.go`)
- **Enriched Prompt-Injection Guard** — enhanced prompt-injection detector with heuristic patterns for raw model delimiters (`<|im_start|>system`, `<<SYS>>`, `[SYSTEM PROMPT]`, `[INST]`), verbatim system prompt extraction attempts, and developer/admin mode override simulations. (`internal/tokensaver/injection.go`)
- **Accurate Gemini Cached Token Tracking** — correctly unmarshals and propagates `cachedContentTokenCount` from Gemini stream and non-stream responses into `OpenAIUsage.CachedTokens`, providing accurate cache hit reporting and cost calculation. (`internal/translator/gemini.go`)
- **Gemini Vision FileData & Audio Modalities** — added support for remote HTTP/HTTPS image URLs (`fileData: { fileUri, mimeType: "image/*" }`), base64 input audio (`input_audio`, `audio_url`), and uploaded documents in Gemini native translator, matching Next.js full multimodal capabilities. (`internal/translator/gemini.go`)
- **Realtime SSE Usage Stream & Topology Animation** — added in-memory in-flight request tracker (`internal/usagetracker`), real-time SSE broadcasting (`GET /api/usage/stream` and `GET /usage/stream`), and recent requests ring buffer matching the Next.js dashboard shape, enabling instant glowing pulse node & marching-ants edge animations on the Usage Topology graph when requests are handled by `9router-go`. (`internal/usagetracker/tracker.go`, `internal/handlers/usage_stream.go`, `internal/handlers/chat/fallback.go`, `internal/handlers/chat/usage.go`)
- **Antigravity Anti-Competitive Prompt Stripping & 429 Prevention** — automatically strips competitor identity phrases (e.g. `"You are a Claude agent, built on Anthropic's Claude Agent SDK."` from Zed IDE and Claude agents) from `system_instruction` and message contents, preventing Antigravity from returning synthetic `429 Quota Exhausted` errors. (`internal/translator/antigravity.go`)
- **Edge Relay URL Rewriting & Header Forwarding** — automatically rewrites `BaseURL` to the relay deployment and injects `x-relay-target` and `x-relay-path` headers when a connection uses a Vercel, Cloudflare Worker, or Deno Edge Relay Proxy Pool. (`internal/handlers/chat/connections.go`)
- **No-Auth Provider Proxy Pool Strategy** — automatically respects `settings.providerStrategies` for no-auth providers (e.g. `mimo-free`, `opencode`), attaching configured proxy pools or rotation strategies to virtual connections. (`internal/handlers/chat/connections.go`, `internal/db/settings.go`)
- **Snake_case Model Limits on `/v1/models` & `/v1/models/info`** — exposes `context_length`, `max_completion_tokens`, `max_input_tokens`, and `max_output_tokens` so clients like Cline, Roo Code, and LibreChat resolve proper context ceilings. (`internal/handlers/chat/chat.go`, `internal/providers/capabilities.go`)
- **CodeBuddy OAuth Configuration** — registered `codebuddy-cn` and `codebuddy-intl` OAuth token refresh configurations. (`internal/providers/oauth.go`)
- **OpenCode Official Client Fingerprint Headers** — injects official headers (`User-Agent: opencode`, `x-opencode-client: desktop`, `x-opencode-session: ses_...`, `x-opencode-request: msg_...`, `x-opencode-project: global`) on free-tier OpenCode requests to prevent rate limiting from unidentified client traffic. (`internal/proxy/opencode.go`, `internal/proxy/executor/providers.go`)
- **Kimchi Dual Authentication** — supports direct API keys (`Authorization: Bearer <key>`) in addition to OAuth tokens with seamless credential resolution. (`internal/handlers/chat/connections.go`, `internal/handlers/chat/kimchi_handler_test.go`)
- **Startup Banner & Version Display** — dynamically displays current version in CLI startup banner (`🚀 9Router Go Proxy (v1.8.3) on :20130`) and server ready logs. (`cmd/9router-go/main.go`)
- **New Provider Registries & Aliases** — added Alibaba Token Plan Singapore (`alitp-intl` / `ali-tp` / `alitp`) and Fish Audio Text-to-Speech (`fish-audio` / `fish`). (`internal/providers/providers.go`, `internal/providers/aliases.go`)

### 🐛 Bug Fixes

- **Invalid Tool Parameters & Decoy Schemas** — provided valid non-empty `properties.reason` schema for all 21 Antigravity decoy tools and mapped `tool_call_id` to exact function names in OpenAI-to-Gemini conversation history, eliminating protobuf validation errors when using Claude Code or other tool-calling clients. (`internal/translator/antigravity.go`, `internal/translator/gemini.go`)
- **Antigravity Upstream Model Resolution** — prevented invalid model aliases like `gemini-3.7-flash-high` from reaching Google Cloud Code without being translated to their backend model IDs. (`internal/translator/antigravity.go`)

### 📚 Documentation

- Comprehensive refresh of `README.md`, `COMPARISON.md`, `DATABASE.md`, `ARCHITECTURE.md`, `TECHNICAL_DEBT.md` (0 open items), and newly added `ROADMAP.md`.

## [v1.8.2] — 2026-08-14

### ✨ Features

- **Antigravity Tool Cloaking & Anti-Ban Decoy System** — automatically cloaks client tool declarations with `_ide` suffixes (e.g. `Bash_ide`), injects 21 official Antigravity IDE decoy tools (`run_command`, `replace_file_content`, `grep_search`, `list_dir`, etc.), synchronizes conversation history functionCall/functionResponse names, and seamlessly uncloaks tool names on response SSE stream and non-stream outputs. (`internal/translator/antigravity.go`, `internal/translator/gemini.go`)
- **Antigravity Native Image Generation** — added image model detection (`imagen`, `*image*`), aspect ratio suffix parsing (`16x9`, `4:3`, `1:1`, custom resolutions via GCD reduction), `requestType: "image_gen"` envelope wrapping with forced non-streaming `/v1internal:generateContent`, and OpenAI-compatible base64 image response formatting. (`internal/translator/antigravity.go`, `internal/proxy/gemini.go`)
- **Edge Relay & Transport Engine** — added transport support for Vercel, Cloudflare Worker, and Deno edge relays using `x-relay-target` and `x-relay-path` headers, wildcard `noProxy` domain filtering, and legacy connection-level proxy configuration fallback (`connectionProxyUrl`). (`internal/proxy/transport.go`, `internal/handlers/chat/connections.go`)

### 🐛 Bug Fixes

- **Proxy Pool DB Parsing Bug** — fixed `GetProxyPool` (`internal/db/proxyPools.go`) failing to parse single string `proxyUrl` created by Next.js UI / `InsertProxyPool`, which previously caused proxy pools to be silently ignored and requests to fall back to direct connections. Added parsing for `type`, `noProxy`, and `strictProxy` metadata.

## [v1.8.1] — 2026-08-12

### ✨ Features

- **Combo strategy sync with Next.js reference** — per-combo rotation state, correct auto-switch ordering, and a capabilities provider (`internal/providers/capabilities.go`) replacing the hardcoded vision/pdf maps with tiered capability detection.
- **Flatten nested combos** — `combo-wombo → free-tier` now expands to its four leaf models, so round-robin actually rotates across them instead of always landing on the first leaf (this was hammering one account and producing the `429 all connections for this provider are rate-limited` error).
- **Turn-aware rotation** — `applyComboStrategy` advances the rotation index only on a new turn; mid-turn tool-use requests reuse the model serving the turn, so the provider never switches mid-turn (which broke Gemini thinking models that require a `thought_signature` on current-turn function calls).
- **Bounded retry-once on total combo 429** — when every combo model fails with a Retry-After ≤ 8s, the pass waits once and retries before surfacing a hard 429 (`comboRetryAfter`).
- **Backfill default `thought_signature`** — every `functionCall` part now carries a `thoughtSignature` (the real one via `__ts__` transport when present, else the Next.js `DEFAULT_THINKING_AG_SIGNATURE`), closing the last 400-`thought_signature` gaps on mixed combos.
- **Gemini tool-schema keyword parity** — strip the remaining unsupported JSON-Schema keywords (`multipleOf`, `uniqueItems`, `contains`, `unevaluated*`, `contentSchema`) and fill bare `{}` schemas with the object placeholder, matching Next.js `cleanJSONSchemaForAntigravity` (fixes `Invalid tool parameters` 400 from antigravity).
- **Sanitize tools on the OpenAI-compat Gemini path** — the `gemini` provider is now marked `gemini-openai` and its `/v1beta/openai` bodies are run through `SanitizeOpenAITools`, so the strict schema validation applies on both Gemini routes.

### 🐛 Bug Fixes

- **Emit camelCase `thoughtSignature`** — the Gemini-native `generateContent` endpoint only recognizes the camelCase part field; the snake_case regression caused the `400 Function call is missing a thought_signature` error. Both read and write directions now handle camelCase.
- **Use the daily antigravity endpoint** — migrate `cloudcode-pa.googleapis.com` → `daily-cloudcode-pa.googleapis.com` for the `antigravity` provider (`providers.go`, `antigravity_project.go`, MITM domain list) to avoid strict rate limits.
- **Combo connection retry-loop parity** — connection retry loop now matches the single-model path.

### 🧹 Chores / Docs

- Remove the stray `patch_combo.go` throwaway script.
- Add design specs and implementation plans for the combo sync / thought_signature / backfill / schema-parity work.

## [v1.8.0] — 2026-08-11

### 🐛 Bug Fixes

- **Combo/router fallback bypasses model-lock backoff on retryable errors → antigravity rate-limit loop** — `handleComboFallback` / `handleMessagesComboFallback` (`internal/handlers/chat/combo.go`) called `tryForwardWithConnection` directly, so `LockConnectionModel` was never invoked on retryable errors (429/500s) — unlike the single-model path `handleAccountFallback`. The exponential 429 backoff was dead in the router path: every request re-tried all combo models back-to-back on the same connection/account, got 429, returned 429, and the client's ~35s retry repeated the loop forever. Fix:
  - New `comboLockRetryable` helper runs on every `RetryableStatusCodes` error in both combo loops — classifies via `ClassifyError`, calls `LockConnectionModel(connID, model, cooldownSec, newBackoffLevel)` so the exponential backoff persists across requests, and appends the conn to a request-local `excludeIDs` passed into `getBestConnection` so remaining combo models don't re-select the same connection (same account = same quota bucket).
  - A locked-connection skip covers pinned connections whose direct-fetch branch bypasses `getBestConnection`'s lock check.
  - `context.Background()` → `ctx` in `handleComboFallback` so client cancels propagate; the 502/503/504 transient-wait sleep is preserved.
  - Test: `TestHandleMessagesComboFallback_429LocksAndExcludesConnection` asserts a 429 locks the connection AND keeps the second combo model from re-hitting it (exactly 1 upstream hit).

## [v1.7.2] — 2026-08-08

### 🐛 Bug Fixes

- **Antigravity 429/404 failure-loop fix** — An unprovisioned Antigravity account
  (`onboardUser` returns `200` with an empty `cloudaicompanionProject`) left the
  connection without a `projectID`. The router then force-refreshed the OAuth
  token on every request (never an auth problem, so it never helped), fell through
  to a guaranteed-404 OpenAI-compatible lane on `cloudcode-pa.googleapis.com`,
  and repeated client retries rammed Google's rate limit (`429`). (`internal/handlers/chat/gemini_handler.go`, `internal/handlers/chat/antigravity_project.go`)
  - `fetchAntigravityProjectID` now reports the outcome (`projectID`, `authFailed`,
    `noProject`). Token refresh runs **only** on a genuine `401/403` — never on a
    missing/empty project.
  - When antigravity has no project ID, it no longer burns a request on the dead
    OpenAI lane; it returns an error and the fallback chain moves straight to the
    next provider.
  - **Negative cache (10 min, per connection):** once Google confirms "no project",
    later requests skip the `loadCodeAssist`/`onboardUser` RPCs entirely — this is
    what stops the repeated `429` hammering.
  - Onboarding guidance is logged once per connection per window
    ("onboard the account via Antigravity IDE/CLI, then re-login"); repeated
    failures log at `Debug` instead of spamming `Warn`. (`internal/handlers/chat/fallback.go`)

### 🧪 Tests

- `antigravity_project_test.go` — pins the probe classification (project found /
  token rejected `401`+`403` / project definitively missing / transient `429`+`503`)
  and the negative-cache expiry semantics. (`internal/handlers/chat/antigravity_project_test.go`)

## [v1.7.1] — 2026-08-08

### 🐛 Bug Fixes

- **Cached-token parity across every provider** — Prompt-cache accounting no longer works only for antigravity. Gemini `usageMetadata.cachedContentToken` now flows through both non-stream and stream translation into OpenAI `usage.cached_tokens`; the `!translate` response path uses a dual-format parser (`ParseResponseUsage`) that reads Claude `cache_read_input_tokens`/`cache_creation_input_tokens` and OpenAI `prompt_tokens_details.cached_tokens`, so cached tokens survive any provider → OpenAI → Claude double translation. (`internal/translator/gemini.go`, `internal/translator/response.go`)
- **Gemini tool-schema `const` re-injection** — `stripUnsupported` now re-runs after `anyOf`/`oneOf` flattening so `const` and vendor `x-*` keys can't leak back into the merged branch. (`internal/translator/schema.go`)
- **Provider 403 is now retryable** — Gemini/antigravity daily-quota errors can arrive as HTTP 403; these now trigger the connection fallback instead of a hard failure. (`internal/providers/providers.go`)
- **CodeBuddy CN stream cleanup** — The stall reader is now closed after the stream, stopping its shutdown watcher + stall timer (no per-request goroutine leak). (`internal/proxy/executor/codebuddy.go`)

### ⚙️ Graceful Shutdown Hardening

- New `internal/shutdown` package: a process-wide signal the first Ctrl+C / SIGTERM fires.
- `StallReader` now closes in-flight SSE upstream bodies on shutdown, so `server.Shutdown` drains streams in milliseconds instead of waiting out the 15s deadline — and the deferred DB/log-file close always runs.
- Translate-path SSE handlers emit a final `data: [DONE]` on abort so clients get a clean end instead of a truncated stream.
- A second Ctrl+C / SIGTERM force-quits immediately (stuck-drain escape hatch).
- Shutdown timeout logs a warning instead of `log.Fatalf`, so `conn.Close()` and the log file are still closed gracefully. (`internal/proxy/stall.go`, `cmd/9router-go/main.go`, `internal/handlers/chat/forward.go`, `internal/proxy/executor/openai.go`)

### 🔧 Internal

- Stream handlers now carry the request `ctx` and pull accumulated usage (incl. cached tokens) out of the translation session, so logged usage reflects real token counts instead of the character-estimate fallback.

## [v1.7.0] — 2026-08-06

### 🚀 New Executors

- **Trae SOLO remote agent** (`internal/proxy/executor/trae.go`) — Port of `open-sse/executors/trae.js`: `POST {base}/chat_sessions` creates a session, `GET {base}/chat_sessions/{id}/events` streams `plan_item` / `token_usage` / `done` as SSE. Cumulative `plan_item.thought` rendering (longest-wins per id, delta-only emission), `Cloud-IDE-JWT` auth, and `work`/`auto`/manual model modes. Non-stream requests aggregate into a single `chat.completion`. Round-trip test: `TestForwardTrae_StreamsAccumulatedThought`.
- **Windsurf gRPC-web** (`internal/proxy/executor/windsurf.go`) — Port of `open-sse/executors/windsurf.js`: hand-rolled protobuf `GetChatMessageRequest` encoder (Metadata.api_key + cascade_id + model_or_alias + repeated messages), gRPC-web framing (0x00 flag + big-endian length), and a `CompletionChunk` decoder (content / done+UsageStats / error) streaming OpenAI SSE. Catalog→wire model alias map ported verbatim; `crypto/rand` session/cascade ids. Non-stream requests aggregate frames into `chat.completion`. Round-trip test: `TestForwardWindsurf_StreamsGRPCWeb`.

### 🎙️ Xiaomi MiMo TTS

- `/v1/audio/speech` for the `xiaomi-mimo` provider now uses the chat-completions contract (port of `open-sse/handlers/ttsProviders/xiaomi-mimo.js`): target text in `role:assistant`, style/language instructions in `role:user`, voice via top-level `audio.voice`, base64 audio from `choices[0].message.audio.data`. (`internal/handlers/media/media.go`)

### ➕ Providers

- **tokenrouter** — Registered as an OpenAI-compatible upstream (`https://api.tokenrouter.com/v1/chat/completions`).

### 🗑️ Removed

- **qwen provider** — Removed from providers, OAuth config, and the alias map (deprecated upstream).

### 📋 Docs

- `TECHNICAL_DEBT.md` — windsurf + trae moved to resolved; zed + devin-cli documented with the safe-stopgap note (devin-cli corrected: ACP over **stdio** subprocess, not HTTP).

## [v1.6.1] — 2026-08-05

### 🐛 Bug Fixes

- **CodeBuddy CN 502** (`internal/proxy/executor/codebuddy.go`) — `codebuddy-cn` / `codebuddy-intl` now use a dedicated executor that forces `stream=true` upstream (CodeBuddy rejects non-stream with HTTP 400 code 11101), injects the CLI/IDE static headers, and re-aggregates OpenAI-chat SSE into a single `chat.completion` for non-stream clients (`sseToOpenAIJSON`, mirroring JS `parseSSEToOpenAIResponse`).
- Provider parity with the reference implementation.

## [v1.6.0] — 2026-08-04

### 🚀 Next.js Engine Feature Ports

- **TTS Voice Listing** (`/audio/voices`) — Full voice-listing with provider support (`edge-tts` default, `elevenlabs`, `gemini`, `local-device`), `?lang` filter, 24h in-process cache, and `byLang`/`languages` grouping matching the dashboard's media-providers page.
- **Proxy-Pools Deploy** (`/proxy-pools/{vercel,deno,cloudflare}-deploy`) — Deploy edge relay functions to Vercel/Deno/Cloudflare with status polling, plus a new `InsertProxyPool` DB method writing byte-compatible `data` JSON.
- **Headroom Management** (`/headroom/*`) — Full headroom-ai lifecycle in Go: binary/Python detection, spawn/stop/restart, compression extras install/uninstall, `/headroom/proxy` reverse proxy with SSRF guard, and dashboard HTML rewrite.
- **CLI-Tools Status** (`/cli-tools/all-statuses`) — Batch detection of 14 CLI tools (Claude, Codex, OpenCode, etc.) installed state + version.
- **Live Console Logs** (`/translator/console-logs`, `/stream`) — In-process ring buffer + SSE streaming of engine log output so the dashboard's "Monitor Console Log" shows Go logs live (25s keepalive, init/line/clear events).

### 🔍 Observability

- **Lightweight Request Tracing** (`/debug/traces`) — In-memory span recording + p50/p95/p99 latency per provider+model with `?n=` cap. Stdlib-only, no OpenTelemetry SDK dependency.

### 🛡️ Security

- **Prompt-Injection Guard** — Heuristic detection (`messages[]`, `input[]`, Claude content blocks) tagging classic injection attempts in logs. Toggle via `--no-injection-guard` / `INJECTION_GUARD_DISABLED` (on by default).
- **`/admin/health/reset` Moved Behind API-Key Auth** — Previously public; now requires a valid API key to prevent unauthenticated health-state resets (open-source hardening).
- **MITM Binds Loopback Only** — TLS proxy binds `127.0.0.1:443` instead of all interfaces, preventing LAN clients from using it as an open proxy.

### 🐛 Bug Fixes & Stability

- **SSE Fragment Rejoin** — Fixed `unexpected end of JSON input` on opencode free-tier by buffering/rejoining truncated SSE JSON payloads per session (1 MiB cap).
- **Codex/CommandCode Tool-Call Streams** — Stable per-call tool IDs/indices (using upstream `call_id`), correct `[DONE]` framing, and checked `w.Write` errors.
- **MITM Goroutine Leaks** — `Stop()` drains in-flight connections (WaitGroup + active conn close); request bodies bounded at 10 MiB.
- **Executor/OAuth Registry Mutexes** — Package-level registry maps now guarded by `sync.RWMutex` (race-free on re-registration).
- **Token Saver JSON Number Preservation** — `CompressMessages`/`InjectSystemPrompt` use `json.Number` so numeric fields (temperature, large ints) round-trip unchanged.
- **`interface{}` → `any`** — Lint cleanup across stream/log packages.

## [v1.5.0] — 2026-07-24

### 🚀 Architecture & Observability Enhancements

- **Modular `main.go` Refactoring** — Extracted CLI subcommands (`mitmEnable`, `mitmDisable`, `mitmStatus`, `resolveDataDir`) to `cmd/9router-go/commands.go` and encapsulated server routing setup into `handlers.SetupServerRouter()`.
- **Structured Request Logging Middleware** — Moved `statusWriter` and `RequestLogger` to `internal/middleware/logging.go`. Requests are logged with Correlation ID (`id=req_...`) using structured logger (`slog.Info`, `slog.Warn`, `slog.Error`).
- **Dynamic HTTP Status Log Levels** — Requests with status 5xx are logged at `ERROR` level, 4xx at `WARN` level, and 2xx/3xx at `INFO` level for clean log filtering in production.
- **Upstream Memory Exhaustion Protection** — Added `io.LimitReader` caps (1MB for upstream error bodies, 10MB for non-streaming completion bodies) to protect proxy memory from rogue upstreams.
- **Double WriteHeader Prevention** — Added `written bool` guard to `statusWriter` and `cw.IsCommitted()` checks across combo fallback handlers to eliminate `superfluous response.WriteHeader` warnings.
- **Typed Request ID Context Key** — Shared `log.RequestIDKey` across middleware and logging packages to ensure context lookups match reliably.

## [v1.4.0] — 2026-07-23

### 🛠️ Technical Debt Remediations (All 9 Items Resolved)

- **Context-based Per-Request Usage Capture** — Replaced global `translator.lastUsage` with context-captured isolation (`WithUsageCapture`, `SetUsage`, `GetAndClearUsage`) to eliminate cross-request data races under concurrent traffic. (`internal/translator/usage.go`)
- **Thread-safe Daily Usage Updates** — Protected `upsertDailyUsage()` with `dailyUsageMu` mutex to prevent concurrent SQLite read-modify-write races. (`internal/handlers/chat/usage.go`)
- **Committed Response Writer** — Wrapped `http.ResponseWriter` with `committedResponseWriter` to prevent safe-retry attempts after response headers have already been sent to the client. (`internal/handlers/chat/response_writer.go`)
- **Strict Context Propagation** — Replaced all `http.NewRequest` with `http.NewRequestWithContext` across handlers, proxy execution drivers, and OAuth helpers to prevent orphaned upstream connections.
- **Graceful Shutdown** — Implemented `http.Server` graceful shutdown with signal drain (15-second timeout) on SIGINT/SIGTERM. (`cmd/9router-go/main.go`)
- **SQLite Connection Pool Optimization** — Reduced SQLite `SetMaxOpenConns(4)` for optimal WAL mode performance and zero connection contention. (`internal/db/client.go`)
- **Thread-Safe ProxyPool Cache** — Added `sync.Map` `proxyPoolCache` in `internal/db/proxyPools.go` to preserve round-robin rotation indices across requests. (`internal/db/proxyPools.go`)
- **Unbounded Request Body Guard** — Added `middleware.MaxBody` (10MB limit) to protect all endpoints from OOM attacks. (`internal/middleware/max_body.go`, `cmd/9router-go/main.go`)

### ⚡ Metrics, Latency & Token Accounting Fixes

- **TTFT & Latency Tracking** — Added `StartTime` and `TTFT` tracking across all streaming and non-streaming proxy execution drivers (`openai`, `opencode`, `deepseek`, `claude`, `grok-cli`, `qoder`, etc.). (`internal/proxy/executor/`)
- **Input Token Calculation Fix** — Added `[]byte` type support to `CountValueChars` so fallback prompt token calculation accurately estimates token size instead of defaulting to 1 token. (`internal/handlers/chat/chat.go`)
- **Output Token Calculation Fix** — Connected `ResponseBuf` in `executor.Request` to record stream output tokens when upstream omits token usage objects. (`internal/proxy/executor/openai.go`)
- **Prompt Caching Tokens Support** — Updated `OpenAIUsage` to extract `cached_tokens` (`prompt_tokens_details.cached_tokens`) and `cache_creation_input_tokens`. (`internal/translator/types.go`, `internal/handlers/chat/usage.go`)

### 🧪 End-to-End Integration Test Suite

- **E2E Test Suite** — Added `internal/handlers/chat/e2e_integration_test.go` to test real HTTP streaming SSE, non-streaming JSON responses, TTFT latency, token accounting, and SQLite DB usage logging end-to-end.

### 🌐 Endpoints

- **`/api/hello`** — Registered `/api/hello` route returning `200 OK` for ping probes from Claude Code CLI. (`cmd/9router-go/main.go`)

## [v1.3.0] — 2026-07-22

### 🏥 Next.js-Compatible Health System

- **Connection-based health** — Replaced old `kv`-based `IsProviderHealthy`/`RecordProviderHealth` with `modelLock_*` fields in `providerConnections.data` JSON blob, matching Next.js `markAccountUnavailable` / `clearAccountError` flow. (`internal/db/health.go`, `internal/db/accounts.go`)
- **Per-connection model locks** — `LockConnectionModel` / `UnlockConnectionModel` / `IsConnectionModelLocked` use SQLite `json_set()` on shared `providerConnections.data`. Dashboard can read/write same fields. (`internal/db/accounts.go`)
- **`IsProviderAvailable`** — New `Repo` method checks if ANY connection for a provider has no active `modelLock_<model>`, replacing the old kv-based pre-check. (`internal/db/accounts.go`)
- **`POST /admin/health/reset`** — Resets `modelLock_*` on connections via query params `?provider=X&model=X`. Dashboard can call via headroom proxy. (`cmd/9router-go/main.go`)
- **Eliminated duplication** — Package-level `IsProviderHealthy` / `ResetProviderHealth` now delegate to `NewRepo(database)` instead of duplicating lock JSON parsing logic. (`internal/db/health.go`)

### 🧪 Test Fixes

- **False-pass assertions** — 3 handler tests were checking old kv-based `repo.IsModelLocked()` which always returned `false` vacuously. Changed to `repo.IsConnectionModelLocked(connID, model)` to actually verify connection-level locks. (`internal/handlers/chat_test.go`)

## [v1.2.0] — 2026-07-22

### 🎯 Gemini Tool Calling Fixes

- **thought_signature round-trip** — Gemini response encodes `thought_signature` into tool call `id` via `__ts__` separator; request decoder restores it for valid verification. Works for both streaming and non-streaming. (`internal/translator/gemini.go`)
- **Antigravity (AGY) support** — Custom `GeminiPart.UnmarshalJSON` handles `thoughtSignature` (camelCase) AND `thought_signature` (snake_case) since the internal `v1internal` endpoint returns camelCase. (`internal/translator/gemini.go`)
- **Tool response name fix** — `tool_call_id` with `__ts__` suffix no longer corrupts `functionResponse.name` extraction, preventing Gemini validation errors on turn 2. (`internal/translator/gemini.go`)

### 🎨 Logging

- **ANSI color-coded logs** — `INF` = green, `WRN` = yellow, `ERR` = red, `DBG` = cyan. Auto-detects TTY (disabled when piped). Disable via `NO_COLOR=1`. (`internal/log/log.go`)

### 🔧 Streaming Fixes

- **SSE multi-line** — Gemini stream chunks with multiple SSE lines (`data: ...\ndata: ...`) are now split and translated individually. Error on one line continues to next instead of aborting. (`internal/handlers/gemini_handler.go`)

### 🧹 Cleanup

- `fallback.go`: Removed misleading `WRN tokensaver failed` logs — replaced with idiomatic `if next, did := ...; did` pattern.
- `test_opencode.go`: Removed (stale temporary test file).
- `internal/translator/gemini_test.go`: Added (unit tests for `thought_signature` round-trip).

## [v1.1.0] — 2026-07-21

### 🚀 New Features

- **SSRF protection** — `/v1/web/fetch` now blocks requests to private/internal IPs (RFC 1918, loopback, link-local, cloud metadata). Matches Next.js `assertPublicUrl()`. (`internal/handlerutil/ssrf.go`)
- **Bypass handler** — Detects Claude Code naming, warmup, and count requests. Returns fake responses without calling upstream, preventing wasted combo rotation slots. (`internal/handlers/bypass.go`)
- **Structured logging** — New `internal/log` package with Info/Warn/Error/Debug levels, runtime config via `LOG_LEVEL` env var. All ~100 `log.Printf` calls replaced across 24 files.
- **Per-connection model locks** — Model locks now stored as `modelLock_<model>` in `providerConnections.data` JSON blob. DB-compatible with Next.js dashboard. Connection A and B can have independent lock states.
- **SSE stall detection** — `StallReader` wrapper closes upstream connection after 6 minutes of no data, preventing hung streams. Integrated into all 4 SSE stream paths.
- **Error classification** — Text-based error rules (8 patterns) + status-based rules (5 codes) + exponential backoff (2s–5min). Fully matching Next.js `checkFallbackError()`.
- **Retry-after tracking** — Tracks earliest `retryAfter` across combo models, includes `Retry-After` header in error responses.
- **Request ID tracing** — Every response includes `X-Request-ID` header, access log includes `id=xxx` prefix.
- **Combo strategies aligned with Next.js** — Sticky round-robin, auto-capability-switch (vision/pdf detection).
- **Health/lock check in combo loops** — Skip unhealthy or locked models during fallback iteration.

### 🔧 Refactoring

- **Error response consistency** — `WriteJSONError` now status-code-aware (e.g., 401 → `authentication_error`, 429 → `rate_limit_error`). `auth.go` inline JSON replaced.
- **SSE consolidation** — `proxy.WriteSSEHeaders` shared by all 4 SSE stream functions. `proxy.SSECopy` with optional `onChunk` callback.
- **Shared test fixture** — `internal/dbtest` package provides canonical `CreateTables()` eliminating duplicated schema in 5+ test files.
- **`stringBuilder` → `bytes.Buffer`** — Removed duplicate custom type in favor of standard library.

### 📚 Documentation

- `ARCHITECTURE.md` — 10 Mermaid flow diagrams (request lifecycle, combo, fusion, error classification, etc.)
- `DATABASE.md` — All 11 tables, JSON blob structure, Go vs Next.js differences

### 🐛 Fixes

- `RetryAfter` ceiling calculation corrected from floor to proper ceiling (`time.Second - 1`)
- Stream translation now handles `[DONE]` marker before JSON parsing
- `TranslateResp` field now passed in `tryForwardWithConnection`

## [v1.0.2] — Previous

- Initial release with OpenAI/Claude SSE proxy, combo fallback, token savers, benchmark results.
