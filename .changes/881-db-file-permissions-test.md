### Prove the database file permissions the credential vault relies on

`OpenDatabase` has chmod'ed the data directory to `0700` and the database file
to `0600` since the permission fix — the database holds provider API keys and
OAuth tokens, so that is the at-rest protection the whole threat model rests
on. Nothing proved it. A regression there is silent on every platform nobody
inspects by hand, and it turns a plaintext-credential store into a
world-readable one.

Three cases, all skipped on Windows where POSIX modes are not enforced and
run for real on the `ubuntu-latest` CI job:

- a fresh open ends with `0600` on the file and `0700` on the directory,
  including the `-wal` and `-shm` siblings the driver creates afterwards —
  those hold the same page images as the main file, so directory access alone
  is enough to recover credentials from them;
- a database left loose by an older build, a manual copy or a restore is
  tightened on the next open rather than left as found, which is why the
  chmod runs on every open and not only on create;
- a data directory that does not exist yet is created private rather than at
  the `MkdirAll` default, which follows the process umask and is commonly
  `0755`.

The assertion helper was itself probed for vacuity: a subtest feeds it a
wrong expectation against a `testing.T` that is not the real one and requires
it to fail. Without that, a mode comparison that always passes would look
exactly like a passing suite.

Closes part of CI-01, which tracks the operational contract the suite does not
yet prove.

### Verified

- `go test -count=1 ./internal/db/` passes.
- `golangci-lint run --new-from-rev="$(git merge-base luqman/main HEAD)" ./...`
  reports 0 issues.
- `go vet ./internal/db/` clean.
