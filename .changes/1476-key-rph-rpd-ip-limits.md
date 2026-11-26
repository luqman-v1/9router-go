### Per-key hourly and daily request ceilings, and a per-client-IP gate

- **Requests / hour and Requests / day** are now real ceilings on an API key,
  alongside RPM, TPM and concurrency. Before this the limiter held one window,
  so a key could sit far under its minute cap all day and still be the one
  draining the upstream account. The hourly and daily windows share one stamp
  list with the minute window, so the three can never disagree about how many
  requests were admitted.
- **A refusal names the window that tripped** (`rpm`, `rph`, `rpd`) and its
  `Retry-After` is computed against that window, so a daily refusal no longer
  tells a client to retry in 60 seconds and get refused all day.
- **A per-client-IP ceiling** (`perIpRpm`/`perIpRph`/`perIpRpd`/
  `perIpConcurrency`) survives a caller minting fresh keys: it is enforced
  against the address under an `ip:` scope, so it can never collide with a key
  id or a provider id. Unconfigured means free: the gate checks its own zero
  value and stores no stamps.
- **The dashboard** carries the two new inputs in the API-key policy modal and
  the plan editor, RPH/RPD badges on the keys table, and the model-visible
  `rph`/`rpd` in the policy PUT response. Screenshots of the three surfaces are
  attached before (base) and after (this branch).
- **The per-IP concurrency counter is bounded.** A concurrency-only `AcquireIP`
  creates `active[ip]` without ever stamping a window, so the sweep in
  `recordStampLocked` never ran for that path and a flood of distinct addresses
  grew the map without limit (a 5000-address probe left 5000 live entries).
  `tryAdmitLocked` now sweeps before it claims a slot, and the sweep triggers on
  the union of the window and concurrency maps. `TestAcquireIPConcurrencyMapStaysBounded`
  pins it.
- Verified: `go test ./internal/middleware/ ./internal/db/
  ./internal/handlers/dashboard/`; the new window and IP tests live in
  `ratelimit_limits_test.go` (a neutral name, so they run on every GOOS; a
  `_windows_test.go` suffix had excluded them from the Linux CI runner), fail on
  base with `CheckWithWindows undefined` / `AcquireIP undefined`, and pass with
  the change. Frontend `bun run build` and the svelte-check ratchet stay green.
