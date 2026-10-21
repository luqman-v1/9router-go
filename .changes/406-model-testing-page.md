### Added

- Model Testing page (`/dashboard/model-test`) with two scopes:
  - **Per model** — every active connection's probeable models (chat plus
    embedding, image, TTS, STT, video and System One); select any subset.
  - **Per provider** — every connected provider, tested once over its chat lane.
- A per-provider row carries the provider, not a model/kind: the gateway picks
  the provider's first chat-lane model from the connected catalog
  (`ChatHandler.FirstChatModelForProvider`) and probes it, so a provider row and
  its failing model row report the same error text.

### How it works

- Each probe reuses the per-model Test button's dispatch
  (`internal/handlers/media/model_test_ping.go`), so a batch verdict and a button
  verdict come from the same endpoint, the same payload and the same pass/fail
  rules. No second copy of the forwarding logic.
- The run is owned by the gateway, not the page
  (`internal/handlers/media/model_test_run.go`): a new `ModelTestRunner` holds the
  sweep on a goroutine tied to a run-level context, so it survives navigation,
  reload and tab close. The page polls `GET /api/model-tests/runs/current` and
  renders the server's own snapshot.
- Endpoints, mounted with the rest of the dashboard REST API and behind
  `RequireDashboardAuth` (`internal/handlers/router.go`):
  - `POST /api/model-tests/runs` — start a run (202), 409 when one is live.
  - `GET /api/model-tests/runs/current` — current/last snapshot.
  - `DELETE /api/model-tests/runs/current` — cancel; unfinished rows read
    `canceled` rather than hanging on `pending`.
- Probes run sequentially: they share the provider accounts the selector rotates
  through, so a parallel sweep would race that rotation instead of measuring it.
- A panicking probe fails its own row, not the process (nil-ChatH relay handler,
  zero-panic policy in AGENTS.md §4.E).
- Both scopes share one set of endpoints: a target is either
  `{"model","kind"}` or `{"provider"}`, so one run can mix them and the page
  polls a single snapshot.

### Verified

- `go test ./internal/handlers/media/` — new table-driven runner tests (validation,
  verdict mapping, counts/order, concurrent-run refusal, cancel finalisation,
  panic isolation, provider targets, and the HTTP handlers) pass.
- `npx svelte-check --tsconfig tsconfig.app.json` reports no error in any file this
  change touches; the existing App.svelte `pageMeta` and unused-import findings are
  pre-existing.
- `make vet-svelte` was not run: `bun` is not installed in this environment, so the
  frontend was type-checked with the underlying `svelte-check` directly.
