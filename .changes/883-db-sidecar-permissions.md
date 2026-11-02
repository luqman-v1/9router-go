### Tighten the WAL sidecars, and prove the modes the credential vault relies on

`OpenDatabase` has chmod'ed the data directory to `0700` and the database file
to `0600` since the permission fix — the database holds provider API keys and
OAuth tokens, so that is the at-rest protection the whole threat model rests
on. Two gaps remained, and neither was visible without a probe:

- **The `-wal` and `-shm` sidecars were never chmod'ed.** SQLite creates both
  on the first *write*, which is after `OpenDatabase` returns, so the modes set
  at open time cannot reach them. They carry the process umask, commonly
  `0644`, and they hold the same page images as the main file — a reader with
  directory access could recover the plaintext credentials from them even with
  the main file at `0600`.
- **Nothing proved any of it.** The existing suite asserted nothing about the
  modes, and the first attempt at a test asserted the sidecars *after an open
  with no write*, so both files were absent and the assertion loop skipped
  them without checking anything. A mode comparison that always passes looks
  exactly like a passing suite.

`RestrictSQLiteModes(dbDir, path)` now re-asserts `0700` on the directory and
`0600` on the main file and every sidecar (`-wal`, `-shm`, `-journal`). It runs
at open time, and again after the schema bootstrap in `internal/app`, which is
the first write and therefore the moment the sidecars come into existence.
Best-effort: a sidecar that does not exist yet is skipped rather than treated
as an error, and on a platform where the mode is not enforced the chmod is a
no-op.

Four cases, all skipped on Windows where POSIX modes are not enforced and run
for real on the `ubuntu-latest` CI job: a fresh open ends at `0600`/`0700`; the
sidecars exist and end at `0600` after the first write; a sidecar left loose is
tightened on the next call; and a data directory that does not exist yet is
created private rather than at the `MkdirAll` default, which follows the
process umask and is commonly `0755`.

The assertion helper was itself probed for vacuity, and the sidecar test was
probed by mutation: dropping the sidecars from the chmod loop turns seven
assertions red, including `db wal` and `db shm`. The sibling-existence check is
asserted before the mode check, so a driver change that stops creating them
fails loudly instead of going vacuous.

### Verified

- `go test -count=1 ./internal/db/` passes.
- `go build ./...` and `go vet ./internal/db/ ./internal/app/` clean.
- Mutation probe: sidecars removed from the chmod loop → 7 assertions red
  (`db file`, `db wal`, `db shm`, both fresh and tighten cases). Restored,
  suite green.
- `gofmt` verified against the git blob, not the worktree — this checkout is
  `core.autocrlf=true`, so the worktree copy is CRLF and `gofmt -l` flags every
  file in the package. The stored blob is clean.
