### Added

- `POST /api/keys/import` imports a client API key idempotently (#1557, ported
  from `diegosouzapw/OmniRoute` `v3.8.54`). The body carries the secret and
  optional `id`, `name`, `machineId` and `allowedModels`; the secret is stored
  as an argon2id verifier plus its SHA-256 lookup hash, never in plaintext.
  Replaying the same body answers `200` with the stored row instead of `409`, so
  a scripted import is safe to retry; a body that would overwrite a different
  secret, or a malformed secret or id, is refused (`409` / `400`) and leaves the
  stored row untouched. The response carries the masked display value and never
  `keyHash` or `lookupHash`.
- `Repo.ImportApiKey` (`internal/db/apikeys_import.go`) with
  `ErrApiKeyImportConflict` / `ErrApiKeyImportInvalid`, writing the key row and
  its allowlist in one transaction on both SQLite and PostgreSQL.

Verified with `go test ./internal/db/... ./internal/handlers/dashboard/...`,
`go vet` on both packages, and `gofmt -l internal/`.