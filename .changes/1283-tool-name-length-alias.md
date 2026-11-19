### Changed

- **Tool name aliasing for 64-char limit providers** — For OpenAI-format providers that enforce a 64-character tool name limit (`openai`, `azure`, `azure-openai`), tool names > 64 chars are now aliased to a deterministic `tool_<sha256[:50]>` before the request leaves the gateway. Ported from OmniRoute #20258 spec.

### Internal

- `translator`: new `FitToolNamesForProvider(body, provider)` entry point with sha256-based aliasing for 64-char-limit providers; `UsesToolNameHash` gating.
- `handler/chat`: `fallback.go` now calls `FitToolNamesForProvider` instead of `FitToolNames` so the alias is applied at the fallback forwarding site.
- `proxy/executor`: `applyToolNameAlias` guard added to `ForwardOpenAI` and `ForwardAzure` so bare OpenAI/Azure paths also alias before the request leaves; fitted map merged into `req.ToolNameMap` for response restoration (streaming via `toolNameRestoringWriter`, non-streaming via `jsonResponse` + `DecloakClaudeResponseBody`).
- `translator/tool_fit_uniqueness_test.go`: pass provider arg to `FitToolNamesInMap` for legacy truncation tests (`deepseek`).
- `translator/tool_fit_test.go`: new tests — `TestFitToolNamesForProvider_OpenAI_SHA256Alias`, `TestFitToolNamesForProvider_NonHashProvider_KeepsTruncation`, `TestFitToolNamesForProvider_RestoreResponse`, `TestFitToolNamesForProvider_NoToolsUnchanged`.

### Verification

- `go test ./internal/translator/...` — PASS
- `go test ./internal/handlers/chat/...` — PASS