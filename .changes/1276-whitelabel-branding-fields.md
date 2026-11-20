### feat(dashboard): expand white-label branding with five new fields

- Settings > White-label branding card gains **Application description** (200 chars),
  **Tagline** (80 chars), **Login logo URL**, **Tray app name** (60 chars), and
  **Tray icon URL**. All five are stored, returned by `GET /api/settings`, and
  read by the surfaces that render them.
- **Login screen consumes the branding.** `LoginView.svelte` now renders the
  tagline beneath the app name and points its logo tile at `loginLogo` when set
  — both read from `/api/auth/status`, which already answers before a session
  exists. The title and logo fall back to `9router-go` and `/favicon.svg` when
  unset.
- **System tray companion consumes its name.** `cmd/9router-tray/gui.go` reads
  `trayAppName` from the gateway's settings row (`GET /api/settings`) and shows
  it as the menu's first item and the tray title. An unreachable gateway, a
  non-200 response, or a blank title all fall back to `9Router Go` (C5), and
  `tray.ico` stays embedded so a broken `trayIcon` URL can never leave the tray
  with no mark.
- `internal/handlers/dashboard/auth.go` adds `appName`, `appTagline` and
  `loginLogo` to the `GET /api/auth/status` payload, so the login screen reads
  them from the one endpoint it already calls.
- `internal/db/settings.go` carries the five fields on `SettingsData` (plus
  `appName`/`appLogoUrl`), so the typed read sees what the settings blob holds.
- `internal/handlers/dashboard/whitelabel.go` adds five normalizer functions
  (`normalizeAppDescription`, `normalizeAppTagline`, `normalizeLoginLogoUrl`,
  `normalizeTrayAppName`, `normalizeTrayIconUrl`) and extends
  `applyWhitelabelUpdate` and `sanitizeWhitelabel` to cover the new keys.
  Login-logo and tray-icon URLs reuse `normalizeAppLogoUrlWithMax` so the same
  HTTPS-or-local-path rule applies.
- `web/src/components/ProfileSettingsView.svelte` loads the five fields in
  `$effect`, `loadSettings`, and `handleSaveAll`; the render block inserts five
  inputs in the existing branding card.
- `internal/handlers/dashboard/whitelabel_test.go` adds table-driven tests for
  all five normalizers and updates `TestApplyWhitelabelUpdateClearsOnBadPayload`,
  `TestApplyWhitelabelUpdateKeepsGoodPayload`,
  `TestSanitizeWhitelabelDropsUnusableStoredValues`, and
  `TestSanitizeWhitelabelKeepsUsableStoredValues` to cover the new keys.

**Verified.** `go build ./...` (including `CGO_ENABLED=0 GOOS=linux`), `go vet`,
`go test ./internal/handlers/dashboard/`, and `bun run build` pass. `bun run
ratchet:svelte` reports 0 unresolved identifiers and 74 errors against a
baseline of 74 — no new errors. `TestRecordConnectionAccountBanFailure_EscalatesAndClears`
fails identically on `luqman/main` at the same commit, so it is not this PR.