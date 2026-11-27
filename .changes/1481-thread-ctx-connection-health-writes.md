### fix(ctx): thread request context through connection health writes

The remaining `*Repo` methods behind connection health — `LockConnectionModel`,
`GetConnectionBackoffLevel`, `LockConnectionRateLimit`,
`RecordConnectionScopedError`, `ClearConnectionRateLimit`, `UnlockConnectionModel`
and the health probes — still took context-less SQLite connections. A
context-less statement holds a write lock for its full duration even after the
caller has gone away: client disconnect, timeout or graceful shutdown cannot
cancel it, and the account rotation path keeps stalling behind it.

- Every handler, executor and integration call site now passes its existing
  `ctx`.
- `LockConnectionModel` in the upstream-error path discards its error like the
  sibling call beside it; that call is best-effort by design.

**Why folded instead of stacked:** the handlers are the only callers of these
methods, so the signature change and the call-site update cannot land as
separate PRs — the lower PR never compiles and can never pass CI.

**Verification** (`go build ./...`, `go vet ./...` clean):

```
$ go build ./... && go vet ./...
$ go test -count=1 ./internal/db/... ./internal/handlers/... ./internal/proxy/executor/...
ok  9router/proxy/internal/db
ok  9router/proxy/internal/handlers
...
```

**Behaviour notes:** `TestSetupServerRouter_SPARoutes` fails on a pristine
`origin/main` worktree with the same 404s on `/sw.js`, `/manifest.json`,
`/manifest.webmanifest` and the provider PNGs when `web/dist` is absent — the
SPA is not embedded without a `make web-build` first. Not caused by this
change; reproduced on base.

**CI fix:** the Race detector job's `-timeout 10m` (and `make test-race`'s
copy) is raised to 20m. The 10m per-package ceiling was already exhausted by
`internal/handlers/chat` on slow runners — 589.8s and 590.7s under `-race` on
commits on `main` on 2026-11-26 (1.6% headroom), so the red check on this PR
was `panic: test timed out after 10m0s`, not a data race: the race detector
itself is unchanged and still runs the full suite.

**Breaking changes:** None. `*Repo` signatures gained a leading
`context.Context`; every in-tree caller is updated in the same commit.
