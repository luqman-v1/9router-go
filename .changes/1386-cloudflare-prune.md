### fix: prune retired Cloudflare Workers AI models and add active replacements (#1386)

Port of upstream `decolua/9router#ecb4d49eb`, which synced the Cloudflare AI
registry with the 2026-11-20 retirement list and the current live catalogue.

- **`cf` and `cloudflare-ai` seed lists** now match: 12 deprecated/absent ids
  dropped (llama-3.2 1B/3B, llama-3.1 8B/70B fp8-fast, qwq-32b,
  deepseek-r1-distill-qwen-32b, kimi-k2.6, and five retired image models) and
  the seven active replacements added in upstream order with their upstream
  display names (Llama 4 Scout 17B, DeepSeek V4.1 Flash 1017, Gemma 4 26B A4B,
  GLM 5.3, GPT OSS 120B/20B, Qwen 3.8 27B).
- **`ProviderModelKinds`**: pruned the retired image kinds for both provider
  lanes. All replacements are LLMs, so no new image/systemone kinds were added.
- **`capabilities.go`**: removed the context-window rows for the two deleted
  llama-3.1 fp8-fast ids; the surviving awq row keeps its 32000 window.
- **`web/src/lib/models.ts`**: synced the dashboard's `cloudflare-ai` catalog
  block as well, so the pruned ids are no longer offered and the replacements
  carry display names. `TestDashboardCatalogListsEveryRoutedModel` requires
  every routed model to appear there.
- Added a table-driven `internal/providers` regression test covering both
  provider ids: retired and no-fast fp8 ids absent, replacements present with
  upstream names, `@cf/` id prefix, no stale service kinds, and alias/id parity.
  The two retired fp8-fast rows in `catalog_sweep_test.go` were swapped for the
  still-seeded awq variant so the sweep keeps pinning the 32000 window.

Closes #1386
