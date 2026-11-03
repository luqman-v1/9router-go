# 9Router Go — Roadmap and Acceptance Criteria

This roadmap is for the current native Go gateway and its embedded Svelte dashboard. It separates near-term hardening from optional product work; it is not a claim of upstream feature parity. The current release metadata is `v1.9.1`. The repository manifest tracks upstream `v0.5.85`; the changelog also contains separately listed `v0.5.86` parity work. That mismatch must be resolved in release metadata before describing a complete upstream baseline.

## Current baseline and known limitations

- Go 1.27, Fx application lifecycle, Chi HTTP server, and SQLite are the runtime foundation.
- The dashboard is a Svelte 5 + TypeScript + Vite SPA. Production serves its generated `web/dist` assets from the Go binary through `//go:embed`; no JavaScript runtime is needed at runtime.
- `web/dist/` is intentionally generated and ignored. It is not guaranteed to exist in a checkout, and the Go package cannot compile without a matching embedded `dist/*` directory.
- The Go database layer bootstraps the upstream core schema additively on startup (`db.EnsureCoreSchema`): a fresh `DATA_DIR` self-boots into a working installation, legacy databases get missing columns backfilled. Not yet implemented: legacy-JSON import, destructive migrations, pre-migration backups, interrupted-migration retry.
- Dashboard login, session cookies, API-key access, and local CLI-token access exist, but they are not a full RBAC/scope system. Management routes must be reviewed against the current middleware rather than assumed to have granular scopes.

## Near-term hardening (priority)

### 1. Schema bootstrap and migrations

**Acceptance criteria**

- ~~A fresh `DATA_DIR` can start the gateway without manually copying an upstream SQLite file.~~ Done: `EnsureCoreSchema` covers empty, current, and legacy-column-missing databases (see `internal/db/schema_test.go`); verified by live fresh-`DATA_DIR` smoke (login → settings → connections → API key).
- An existing upstream-compatible database is upgraded in a transaction, preserves rows and JSON data, and can be retried safely after a failed upgrade.
- Startup fails with an actionable error when the database is incompatible; it must not silently continue with missing tables or partial migrations.
- Remaining: legacy-JSON import, pre-migration backup, interrupted-migration retry test.

### 2. Auth scopes and protected operations

**Acceptance criteria**

- Define and document the scope model for anonymous health/version endpoints, client API keys, dashboard sessions, local CLI tokens, and administrative operations.
- Enforce the scope at the router boundary and deny by default; a valid client API key must not grant update, shutdown, database import/export, or other administrative operations.
- Add route-level tests for anonymous, wrong-scope, read-only, session, CLI-token, and administrative requests, including disabled keys and expired sessions.
- The default password and remote-access protections remain covered by tests, and the security contract is reflected in the dashboard and deployment documentation.

### 3. Race, shutdown, and concurrency reliability

**Acceptance criteria**

- `go test -race ./...` passes in CI without known races, including concurrent database access, OAuth/session state, usage tracking, SSE streams, shutdown, and cross-process lease acquisition.
- Cancellation propagates from clients and server shutdown; no goroutine, upstream body, ticker, or SSE stream remains after the process stops.
- SQLite busy/lock, provider timeout, malformed upstream response, and partial write scenarios have deterministic tests and bounded error handling.
- The race and shutdown tests are runnable with pinned toolchain instructions and are not disabled merely because they are flaky.

### 4. Go coverage baseline

**Acceptance criteria**

- CI produces a package coverage profile from the full Go test suite and publishes the result for review.
- The repository records a baseline and a non-regression rule; any reduction requires an explicit review rather than silently lowering or ignoring the result.
- New security, routing, migration, and concurrency changes include tests for the affected behavior, including error and boundary cases.

**Status (2026-11-03): the first two criteria are met.** The `coverage` job runs
`go test -covermode=atomic -coverprofile=coverage.out ./...`, uploads the profile
as an artifact, and fails when `scripts/coverage-gate.sh` reports a total below
`.github/coverage-baseline.txt` (68.0%; measured 68.7% on `main` at that commit).
The third criterion is unchanged and still open — it is a per-change expectation,
not something a CI job can decide.

### 5. Frontend test and type-safety gate

**Acceptance criteria**

- `web/package.json` exposes an explicit test command, and CI runs the frontend tests as well as the Vite build.
- Tests cover SPA route parsing, provider/model filtering, API error handling, authentication state, and the highest-risk connection/combo/media flows.
- `bun run lint`, `bun run build` (TypeScript project build plus Vite), and the frontend test command pass from a clean dependency install.
- A dashboard change is exercised against a running Go gateway before release; source-level tests alone do not count as UI verification.

### 6. Release supply chain

**Acceptance criteria**

- Every release builds the frontend before embedding it, builds the Go binary with the declared version, and runs the required Go and frontend gates.
- Release artifacts are cross-compiled from a clean checkout, accompanied by checksums, and accompanied by the commit, Go/Bun versions, target OS/architecture, and build command.
- CI verifies that `VERSION`, `version.json`, release tags, and published names are synchronized; the upstream `v0.5.85` versus `v0.5.86` metadata discrepancy cannot pass silently.
- GitHub Actions and container/base-image inputs are pinned or otherwise reviewable, and release notes state known limitations and migration requirements.

### 7. Reproducible benchmark evidence

**Acceptance criteria**

- A benchmark run records the machine, OS, Go version, upstream/mocked upstream behavior, SQLite fixture, request count, concurrency levels, commit, and raw output.
- Evidence covers both streaming and non-streaming requests and reports throughput plus latency percentiles; memory and startup claims are reported only when actually measured.
- Results are compared only when both implementations use the same fixture and workload. Historical `benchmark/RESULTS.md` numbers are evidence from their recorded run, not a current performance guarantee.
- Any regression threshold is defined from repeated measurements before it is used as a release gate; no percentage improvement is promised without a current run.

## Optional product features (not near-term commitments)

These proposals remain useful product directions, but they do not replace the hardening work above and are not claims of current support:

- **Prompt caching:** exact-key response cache with a documented invalidation and privacy policy; acceptance includes cache-hit correctness, TTL behavior, bounded memory/storage, and no cross-tenant leakage.
- **Cost-aware routing:** route only after a measurable workload and quality policy is defined; acceptance includes explainable decisions, user-configurable limits, and benchmark evidence rather than a guaranteed cost reduction.
- **Webhook alerts:** alert on configured upstream/rate-limit/usage events; acceptance includes deduplication, retry/backoff, secret-safe payloads, and opt-in configuration.
- **Quota and tenant controls:** per-key or per-user usage limits; acceptance includes isolation, concurrency-safe accounting, revocation, and dashboard/API parity.
- **Secret/PII redaction:** explicit detection rules with safe defaults, opt-in behavior, false-positive tests, and guarantees about what is or is not sent upstream.
- **Deeper analytics:** p50/p95/p99 latency, token efficiency, and provider/model breakdowns; acceptance includes bounded retention, timezone/cost semantics, and exportable evidence.

## Definition of done

A milestone is complete only when its acceptance criteria are demonstrated in CI or an observed local smoke test, affected documentation is updated, and any accepted limitation is stated plainly. Optional product features may follow only after the relevant reliability, security, and data-compatibility risks are understood.
