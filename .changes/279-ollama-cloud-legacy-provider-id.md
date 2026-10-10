### Fixed
- `ollama-cloud` provider connections were stuck on a legacy provider id that
  never existed in the catalog (only `ollama` does — the id came from a
  sibling project's naming, synced in by hand). Chat requests on those
  connections failed with `has no baseUrl in connection data and is not in
  KnownProviders`, and the Analytics provider topology card rendered the
  connection's account email as the node label instead of a provider name.
  `MigrateLegacyProviderIDs` now rewrites `ollama-cloud` → `ollama` on every
  boot (idempotent, best-effort — a failure logs and does not block
  startup), and `topologyName` in `AnalyticsView.svelte` rejects any
  fallback label containing `@` so an OAuth account email can never stand in
  for a provider name again, independent of the email-privacy toggle.
- Verified: `go test ./internal/db/...` (new
  `TestMigrateLegacyProviderIDs`/`TestMigrateLegacyProviderIDsNilDB`),
  `bun test src` (353 pass / 0 fail), `make vet-svelte` (0 unresolved
  identifiers, baseline tightened 83 → 82).
