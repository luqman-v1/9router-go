### Added

- **New providers**: DaoXE (`daoxe` / `dx`), NaN.Builders (`nan`), OpenAdapter (`openadapter` / `oad`)
  - All three are OpenAI-compatible free-tier providers
  - Support standard OpenAI models: `gpt-4o`, `gpt-4o-mini`, `gpt-3.5-turbo`
  - Include vision, reasoning, search, and tool capabilities for supported models
  - Registered in provider catalog with correct BaseURLs and auth schemes
  - Frontend metadata added for dashboard display (icons, colors, notices)

- **Provider aliases**: `dx` → `daoxe`, `nan` → `nan`, `oad` → `openadapter`
- **Registry aliases**: `daoxe` → `dx`, `nan` → `nan`, `openadapter` → `oad`
- **Model capabilities**: Added per-model capability entries for all three providers in `capabilities.go`
- **Built-in models**: Added dashboard model entries for `dx`, `nan`, `oad` (gpt-4o, gpt-4o-mini, gpt-3.5-turbo, claude-3.5-sonnet, gemini-1.5-pro, llama-3.1-70b)
- **PNG icons**: Added 104x104 PNG artwork for all three providers in `web/public/providers/`
- **Unit tests**: Added `TestNewProvidersRegistry_v060` covering provider registration, alias resolution, RegistryAliases, model catalog, and capabilities

### Fixed

- **NaN.Builders naming**: Canonical provider ID is now `nan` (was `nanbuilders`); removed all `nanbuilders` references

### Verified

- `gofmt ./...` — clean
- `go build ./...` — compiles cleanly
- `go vet ./...` — no issues
- `go test -count=1 ./internal/providers/...` — all provider tests pass
- `go test -tags=integration -count=1 ./internal/integration/...` — integration tests pass
- `cd web && bun test src/lib/providers.test.ts` — 100% pass
- `cd web && bun run ratchet:svelte` — 0 unresolved identifiers, 78 errors (baseline maintained)