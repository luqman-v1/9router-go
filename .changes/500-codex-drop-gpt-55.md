### fix(codex): drop retired gpt-5.5 models, update image and auto-ping defaults

Upstream decolua/9router commit 7878f53b (PR #4852).

- **`gpt-5.5` is retired from Codex with ChatGPT sign-in** (2026-10-14), causing the backend to reject requests with HTTP 400.
- Removed `gpt-5.5`, `gpt-5.5-review`, `gpt-5.5-image`, and `gpt-5.3-image` from the Codex and CX registries in `internal/providers/registry_models.go` and `web/src/lib/models.ts`.
- Routed Codex image generation main model through `gpt-5.6-sol` (`CodexImagesMainModel`).
- Added `CodexAutoPingModel = "gpt-5.6-terra"` and updated QuotaTrackerView tooltip to drop the obsolete `gpt-5.5` mention.
- Verified other providers offering `gpt-5.5` (such as `bazaarlink`, `bb`, `blackbox`, `bzl`, `llm7`, `openai`, `ocz`, `opencode-zen`, `pa`, `perplexity-agent`, `tokenrouter`) remain intact.
