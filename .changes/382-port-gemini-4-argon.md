### feat(providers): port upstream Gemini 4 Argon capabilities, Antigravity client bump, and OpenCode Go wire transport

Port of upstream `decolua/9router` PRs #4887, #4934, and #4991: full Gemini 4 Argon support across capabilities, pricing, thinking levels, Antigravity quota isolation, client fingerprinting, and OpenCode Go Google Generative AI wire transport.

**Upstream References:**
- `decolua/9router#4887`: Gemini 4 Argon capabilities, intro pricing ($2/$10), registry entries, and thinking level clamping.
- `decolua/9router#4934`: Gemini 4 Argon under Antigravity (`gemini-4-argon`, `high`, `medium`, `low`), `ANTIGRAVITY_IDE_VERSION` bump to `2.23.0`, `ANTIGRAVITY_CLI_USER_AGENT` (`1.4.0`), weekly/session quota isolation, and Svelte dashboard normalization.
- `decolua/9router#4991`: Native Google Generative AI wire transport on OpenCode Go (`/inference/go/v1/models/gemini-4-argon-contributor:(stream)GenerateContent`) with `x-goog-api-key`, plus newly released models (`claude-haiku-5-5`, `longcat-2.5-preview-free`, `step-5-preview`, `beam-free`).

**What changed:**
- `internal/providers/capabilities.go`: Added `PDF: true` to `Capabilities` under pattern `*gemini*argon*`.
- `internal/pricing/tables.go`: Added introductory pricing entries for `gemini-4-argon`, `gemini-4-argon-high`, `gemini-4-argon-medium`, and `gemini-4-argon-low` ($2/1M input, $10/1M output, $0.10/1M cached, $10/1M reasoning, $2/1M cache creation).
- `internal/providers/thinking_levels.go`: Added `ClampGeminiLevel` to clamp unsupported thinking levels on Gemini 4 Argon (`minimal` clamped to `low`).
- `internal/providers/client_identity.go`: Added Antigravity CLI and IDE version constants (`AntigravityCLIVersion = "1.4.0"`, `AntigravityIDEVersion = "2.23.0"`, `AntigravityCLIUserAgent`).
- `internal/providers/registry_models.go`: Registered `gemini-4-argon` on `gemini`, `gemini-cli`, `gc`, `vertex`, `antigravity`, and `ag`. Added `claude-haiku-5-5`, `gemini-4-argon-contributor`, `longcat-2.5-preview-free`, `step-5-preview`, and `beam-free` to `opencode-go` and `ocg`.
- `internal/providers/model_formats.go`: Declared `FormatGemini = "gemini"`, added `openCodeGoModelFormats` for `opencode-go` and `ocg`, and mapped `gemini-` prefix to Gemini wire target.
- `internal/translator/antigravity.go`: Added Gemini 4 Argon tiered mappings in `AntigravityModelSynonyms` and `antigravityUpstreamModelIDs`.
- `internal/handlers/chat/antigravity_quota.go`: Added dedicated `argon_weekly` and `argon_session` parsing and quota isolation so Gemini 4 Argon limits do not conflate with or block standard Gemini models.
- `internal/handlers/dashboard/usage_providers.go`: Added Argon family configuration and weekly overlay reconciliation. Bumped Antigravity User-Agent and client version headers to `providers.AntigravityCLIUserAgent` and `providers.AntigravityIDEVersion`.
- `internal/proxy/executor/providers.go`: Implemented `forwardOpencodeGoGemini` to route Gemini wire models on OpenCode Go to `/inference/go/v1/models/<model>:(stream)GenerateContent` with `x-goog-api-key`. Added `claude-haiku-5-5` to `opencodeGoMessagesModels`.
- `internal/mitm/handlers/antigravity.go`: Updated `antigravityIDEVersion` override to `2.23.0`.
- `web/src/components/quota/types.ts`: Updated Antigravity quota normalization in dashboard to parse and render dedicated Gemini 4 Argon weekly/session quotas.

**Verification:**
- `go test ./internal/providers/... ./internal/pricing/... ./internal/proxy/executor/... ./internal/translator/... ./internal/mitm/... ./internal/handlers/chat/... ./internal/handlers/dashboard/...` passed.
- `go vet ./...` passed with 0 warnings.
- Frontend SPA `cd web && bun run build` and `cd web && bun run ratchet:svelte -- --tsconfig tsconfig.app.json` passed with 0 unresolved identifiers and maintained 83-error baseline.
