### Changed

- **Tool name aliasing for 64-char limit providers** — For OpenAI-format providers that enforce a 64-character function-name limit (`openai`, `azure`, `azure-openai`, `azure-ai`), a tool name that the upstream would reject is now aliased to `${safePrefix}_${sha256(name)[:12]}` before the request leaves the gateway. Ported from OmniRoute #20258 (`normalizeOpenAIToolNames`).
  - `safe` is the original name with every character outside `[A-Za-z0-9_-]` replaced by `_`, so a name carrying a dot, a slash or a space is repaired even when it fits the length limit.
  - `safePrefix` is `safe` truncated to `64 - 12 - 1 = 51` chars; the alias is therefore always at most 64 chars.
  - `hash12` is the first 12 hex chars of `sha256(original)`, so the alias is deterministic and the original name is recoverable from the alias alone.
  - An alias that collides with another tool's name falls back to the bare hash, then to a truncated hash with a numeric suffix — every slice is clamped to `len(s)`, so a short fallback alias can never panic the way the previous `alias[:60]` did on a 53-char alias.

### Internal

- `translator`: `UsesToolNameHash` is now the single source of truth for which providers enforce the limit, backed by `toolNameMaxLength` (openai declares 64 in the registry; azure-openai and azure-ai have no registry entry and get the built-in 64, matching upstream's `BUILT_IN_TOOL_NAME_MAX_LENGTH`). The executor's duplicate `toolNameHashProviders` list is gone.
- `translator`: new `FitToolNamesForProvider(body, provider)` entry point; `collectToolNames` also collects short names whose characters violate the charset for hash providers, so the constraint is enforced on the charset and not only on the length.
- `handler/chat`: `fallback.go` calls `FitToolNamesForProvider` instead of `FitToolNames` so the alias is applied at the fallback forwarding site.
- `proxy/executor`: `applyToolNameAlias` guard in `ForwardOpenAI` and `ForwardAzure`; fitted map merged into `req.ToolNameMap` for response restoration (streaming via `toolNameRestoringWriter`, non-streaming via `jsonResponse` + `DecloakClaudeResponseBody`).
- `translator/tool_fit_uniqueness_test.go`: pass provider arg to `FitToolNamesInMap` for legacy truncation tests (`deepseek`).
- `translator/tool_fit_test.go`: hash-alias tests pin the `${safePrefix}_${hash12}` wire format (ends in `_<12 hex>`, max 64), cover all four providers including `azure-ai`, invalid-char names, prefix collisions and an alias shadowing a declared short name; `TestFitToolNamesForProvider_NonHashProvider_KeepsTruncation` still pins the legacy `_N` path for `deepseek`.

### Verification

- `go test ./internal/translator/...` — PASS
- `go test ./internal/proxy/executor/...` — PASS
- Verified the new tests fail on the previous implementation: the old `alias[:60]` slice panics with `slice bounds out of range [:25] with length 12` on a fallback alias, and the old `tool_<sha256[:50]>` shape fails the new format assertion.