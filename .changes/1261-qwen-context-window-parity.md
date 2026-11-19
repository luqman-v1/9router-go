### Fixed
- **Qwen models published a 131k context window instead of their real one**
  (`nous/qwen4-max` reported 131072 against the 1000000 the provider's own
  `/v1/models` declares). The Go `*qwen*` pattern rows carried no
  `ContextWindow`/`MaxOutput`, so every qwen id fell through to the substring
  table's 131072/8192 floor. Synced the whole qwen family from upstream
  `open-sse/providers/capabilities.js` (merged master `aba4c45d`..`1855aa44`,
  v0.5.119): `*qwen*max*`/`*qwen*coder*`/`*qwen3.5–3.7*`/`*qwen*plus*` → 1M,
  `*qwen*vl*`/`*qwen*omni*`/`*qwen*235b*`/`*qwen*` → 262144, `*qwq*` → 131072,
  with upstream's `maxOutput` and the `*qwen*max*` vision flag. The Go-only
  `*qwen3.8*` row keeps its unset thinking format (documented divergence) and
  gains the family's 1M/65536 limits.
- **"Import from /models" dropped the limits normalized catalogues declare.**
  The import read only snake_case `context_length`/`max_completion_tokens`;
  the nous/qoder/zed catalogues emit camelCase `contextLength`/
  `maxOutputTokens`, so an imported `nous/qwen4-max` row saved no window and
  `/v1/models` kept publishing the name guess. The import now reads both
  spellings and persists them as the row's declared limits, which outrank
  every static table.
- **The provider's own /v1/models feed now drives published limits.** A new
  live-catalog resolver reads the OpenAI-style models feed for every provider
  that declares one (nous, openrouter, tokenharbor, sumopod, nvidia, …) and
  publishes each entry's own `context_length`/`max_completion_tokens` (or the
  camelCase equivalents) as authoritative limits on `/v1/models` and the combo
  seat walk — the LLM provider is the source of truth for its windows, not a
  static guess. The feed replaces the static catalog like the kiro/grok-cli
  resolvers, so models newer than the registry snapshot are discoverable; on
  any fetch failure the static registry remains the fallback. Cached 5 min
  per credential with singleflight, bounded like every other live resolver.
- **Custom (openai/anthropic-compatible) nodes now keep their feed's windows.**
  `fetchCompatibleNodeModels` parsed ids only (`parseOpenAIStyleModelIDs`), so a
  custom endpoint that declared `context_length`/`limit.context` still fell to
  the static family guess. It now uses the same `parseModelsFeedLiveModels` the
  provider feeds use, marking stated limits authoritative; a node that states
  none keeps the static resolution. The node's own baseUrl is always the source
  (the compatible dispatch case precedes the registry-feed case), never the
  upstream provider's catalog. Pinned by
  `TestFetchCompatibleNodeModelsKeepsDeclaredWindows`.
- **Free-tier models published floor windows.** Every catalogued free model
  was audited; the ones whose family pattern mis-guessed now carry their real
  per-gateway limits: OpenCode free lane `ling-3.0-flash-fin-free` (262144),
  `nemotron-3-ultra-free` (1M), `nemotron-3.5-lightning-free` (262144) — all
  were 128k/4096; Cloudflare Workers AI `@cf/*` ids now publish the gateway's
  truncated windows (qwen2.5-coder 32768, qwq 24000, r1-distill 80000,
  llama-3.3-fp8 24000, gemma-4 256000, kimi-k2.6 262144) instead of the vendor
  API window or the floor; NVIDIA `google/gemma-4-31b-it` (256000) and
  `openai/gpt-oss-120b` (131072/131072). Sources: the provider's live feed
  (nous), models.dev per-gateway entries (opencode/cloudflare/nvidia). Thinking
  shapes stay on upstream's pattern resolution (fixture-pinned). Pinned by
  `TestFreeModelContextWindows`.
- **OpenCode Zen / Go now match the free lane's windows.** The live-feed
  resolver reads the registry `modelsFetcher` too (zen `/zen/v1/models`, go
  `/inference/go/v1/models`) and understands their `limit.context`/`limit.output`
  shape, so a connected zen/go account publishes the provider's own per-model
  windows. Static fallbacks pinned for the ids the family patterns mis-guess:
  zen free `ling-3.0-flash-fin-free` (262144), `nemotron-3-ultra-free` (1M),
  `nemotron-3.5-lightning-free` (262144); go `hy4-preview` (1024000), `hy3`
  (256000), `muse-spark-1.x-contributor` (1M/131k), `beam-free` (1M); and the
  global `claude-fable-5` exact (1M/128k, was 128k/4096). Only windows are
  pinned per lane — maxOutput/thinking stay on the upstream-synced family
  resolution so the 1829-pair fixture stays green. Pinned by
  `TestOpenCodeZenGoWindows` and `TestOpenCodeGoLiveFeedPublishesProviderWindow`.
- **Full-catalog sweep (2309 provider×model entries).** Audited every published
  entry against the provider's live feed, models.dev per-gateway entries, and
  the upstream tables. Fixed the catalog reader (`GetCatalogLimits` normalized
  ":" before "/" so `hf:openai/gpt-oss-120b` became `hf` and missed the key;
  added `together→togetherai`, `github→github-copilot`, `kilo-gateway→kilo`,
  `codex→openai` provider aliases). Pinned real windows for ~90 more entries
  that sat at the 128k/4096 floor: gpt-oss (131072), Doubao Seed 2.0 family
  (256000/262144 across ark/byteplus/sumopod), llama-3.3-70b / llama-3.2-3b
  (131072 across hyperbolic/nebius/fireworks/cerebras/openadapter/kenari),
  nemotron-3-super/super-vl/ultra-fp4 (262144/1M), dots-3-note-preview (512000),
  Cursor claude-4.5/4.6 (200000/1M), perplexity-agent claude-sonnet-4-6 (1M),
  muse-spark contributor (1M), hcnsec reseller models, pixtral-12b (128000),
  Cloudflare llama-3.1 fast/awq (32000). Reasoning/thinking flags stay on the
  upstream-synced resolution (1829-pair fixture green). Pinned by
  `TestCatalogSweepNoFloorWhereSourceExists` / `TestCatalogLookupProviderAliases`.
  Remaining floor entries are non-LLM (image/stt/tts/embedding/video — window
  N/A), web-lane/router modes, ids whose floor IS the true window (gpt-4o,
  sonar, claude-haiku-4.5), or ids no source publishes a window for yet.
- **Whole-catalog token-limit sync from upstream v0.5.119.** The same
  omitted-limits class affected every family whose Go pattern/exact/provider
  rows declared no numbers: gemini-2/3/4 (1M), claude opus/sonnet 5.x and
  4.6–4.8 exacts (1M/128k, plus PDF input), kimi-k3/k2 (1M/262k), glm-5.2 /
  glm-5.3-flash exacts (1M/131k), deepseek-v4 family (1M/384k), minimax-m3 /
  m2.x (1M/204800), mimo-v2.5/2.6 (1M/131k), grok-4.5/4.6 (500k), gpt-4.1
  (1M/32768), gpt-5 generic (400k/128k), llama-4 (1M), laguna-s paid (1M),
  muse-spark (1M/131k), hunyuan/hy3 (262144/262144), and the 128k floors for
  mistral/command/sonar/step/nemotron/ling. Provider tables synced: nvidia
  (13 rows incl. the missing nemotron/llama-guard/riva entries), kiro gpt-5.6
  (272k/128k), codex `gpt-6.1-terra` (500k, was missing), poolside, ollama
  cloud, opencode-go glm-5.3-flash, codebuddy-cn deepseek-v3-2-volc,
  sensenova-intl (262144/65536 + the missing 6.7-flash-lite row). Added the
  missing `claude-haiku-5-5` / dotted `claude-sonnet-5.5` / `union-alpha`
  exacts and the `*gpt-6*image*` pattern; moved `*gpt-5*codex*` ahead of the
  numbered tiers so `gpt-5.6-codex` publishes 400k, matching upstream order.
  Deliberate Go divergences (grok-4.7 probe, fable adaptive, gemini-web,
  codebuddy-cn EOL rows kept routable) are unchanged.
  Contract tests that pinned the old guesses were updated to the synced
  truth: combo seat-limit minima (deepseek-chat 128000, claude-sonnet-4-6
  1M/128k, gpt-4o 16384 output) and the zed haiku-5-5 test — upstream
  8aef7480 made that id adaptive-only globally, so the anthropic lane no
  longer keeps the budget shape the old test asserted.

### Verified
- `go test ./internal/providers/ ./internal/handlers/... ./internal/translator/... ./internal/proxy/...` — green,
  including the new `TestQwenFamilyWindows` / `TestQwenMaxVision` and the
  1829-pair thinking-levels fixture (one documented post-v0.5.109 divergence:
  nvidia nemotron openai levels, verified against upstream master).
- `go test -tags=integration ./internal/integration/...` — green, including the
  new `TestNousQwen4MaxPublishesRealWindow` (real router: imported row publishes
  1000000/131072, undeclared sibling publishes the family 1000000/65536).
- `cd web && bun run build`, `make vet-svelte` (0 unresolved, baseline 74),
  `go test ./web/...`, `bun test src scripts` (845 pass).
- Live-feed resolver: `TestParseModelsFeedLiveModels` /
  `TestResolveLiveCatalogModelsFeed` (unit) and
  `TestNousLiveFeedPublishesProviderWindow` (integration, fake provider feed:
  `/v1/models` publishes the feed's 1000000/131072 with no import or custom row).
- Live e2e against the real provider feeds (`9ROUTER_LIVE_TESTS=1`,
  `TestLiveE2E_ModelsFeedWindow`): nous `qwen/qwen4-max` and `:free` resolve
  1000000/131072 authoritative from the live `/v1/models`; opencode-zen
  `mimo-v2.6-flash-free` resolves 200000/32000 with the operator's credential;
  the static fallback cross-check publishes 1000000/65536. opencode-go skips
  when no active connection is configured. Offline runs skip cleanly.

### Deliberate divergence from upstream
- Upstream `fetchCompatibleModelIds` returns ids only and
  `LIVE_MODEL_RESOLVERS` covers a fixed provider set (kiro/qoder/github/
  clinepass/cursor/zed/commandcode/…), deriving windows from
  `getCapabilitiesForModel`. This port instead treats **the provider's own feed
  as the window source** for every provider that publishes one (nous,
  openrouter, tokenharbor, opencode zen/go, …) and for custom compatible nodes,
  marking stated limits authoritative. The user's standing requirement is that
  the LLM provider itself — not a static family guess — is the source of truth
  for context windows; the static tables remain the fallback when a feed is
  unreachable or states no limit.