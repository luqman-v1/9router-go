# free-pi upstream contract fixtures

Verbatim shapes of every `api.freepi.ai` response this provider depends on. These are
**reference documents**, not golden test data: they exist so the wire contract stays readable
and reviewable in one place, and so each phase's tests can build fakes from a single source of
truth instead of re-deriving shapes from memory.

Provenance for each file is recorded in its own `_source` field. Two repos were used:

- `dennisonbertram/free-pi-cli` — the official CLI. Authoritative for the wire contract it
  actually dials (`src/provider.ts`, `src/auth-flow.ts`, `src/update-check.ts`,
  `src/usage-tool.ts`, `src/commands.ts`, `src/consent.ts`).
- `hungkevin/free-pi-cli-proxy` — a working third-party proxy against the same upstream.
  Authoritative for operational behaviour the CLI never hits: the **4-session cap**, the 409
  reset-and-retry remedy, the 429 error-code taxonomy, and the ad-impression gate
  (`server.mjs`).

No fixture here is a guess. Where a shape is inferred rather than observed, the `_source` field
says `inferred` and names the basis.
