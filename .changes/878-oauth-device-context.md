### OAuth device flow honours request context; technical-debt register corrected

Two things: the OAuth-01 risk is closed, and the register that tracks it is
brought back in line with the tree.

#### OAuth-01 — every outbound call in the device flow carries the caller's context

`internal/handlers/oauth/device.go` and its siblings built requests with
`http.NewRequest`, so client cancellation, gateway shutdown and deadlines had
nothing to cancel. A client that hung up mid-login left a goroutine and an
upstream connection parked until the 15-second client timeout expired — and
the same applied to the shutdown path, where nothing aborted an in-flight
device exchange.

- `postJSON`, `postForm` and `getJSON` — the three primitives every provider
  family funnels through — now take a `ctx` first parameter and build their
  requests with `NewRequestWithContext`.
- The per-provider start and poll wrappers thread it through, and so do the
  nested helpers that were easy to miss: `glmResolveCodingPlanKey`,
  `glmExchangeBusinessToken`, `museAccountEmail`, `museMintKey`,
  `kiroProfileArn` and `validateKiroAPIKey`.
- The same fix was applied to the inline requests in `special.go`
  (Kimchi, GitLab PAT, iFlow), `kiro_import.go`, `trae.go`, `windsurf.go` and
  `zed.go`, which had the same defect outside the device flow.

`device_context_test.go` pins the contract: cancellation and deadline abort
each primitive, and a live-context control proves a broken request cannot pass
as a cancelled one.

#### DOC-01 — the debt register no longer overstates open risk

`TECHNICAL_DEBT.md` still declared `v1.9.1` and listed three rows as open long
after their fixes landed. A register that overstates open risk sends the next
reader auditing work that is already done; one that understates it hides live
risk. Each row was re-verified against the current tree before this revision:

- **DB-02** — closed. The AES-256-GCM envelope vault (`internal/db/vault.go`,
  `internal/vault/`) seals credentials before every data write and hydrates
  them on read. The remaining limitation is stated rather than dropped: the
  vault is opt-in, so a deployment without `ROUTER_MASTER_KEY` is still
  plaintext at rest.
- **OPS-01** — closed. A manifest carrying no sha256 is now a dead end:
  `PerformSelfUpdate` refuses it, and the GitHub Releases fallback supplies
  version metadata only, never an unverified binary. The fallback still has no
  signed attestation, which is recorded as the remaining gap.
- **DATA-01** — closed. `PruneRequestDetails` bounds both age and row count
  and runs on a timer from `StartRetentionLoop`, wired in
  `internal/app/server.go`. What remains is stated: retention bounds how long
  content lives, not what it holds — there is still no redaction of
  secret-like strings inside a stored message.
- **OAuth-01** — closed by the change above.

COMP-01 (OAuth scope preservation) and DB-03 (multi-process writes) are still
open and are left as they were.

### Verified

- `golangci-lint run --new-from-rev="$(git merge-base luqman/main HEAD)" ./...`
  reports 0 issues.
- `go vet ./...` and `go vet -tags=integration ./internal/integration/...`
  clean.
- `go test -count=1 ./internal/handlers/oauth/` passes, including the five new
  cancellation/deadline/control cases.
