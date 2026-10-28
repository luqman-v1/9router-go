### Scheduled model health checks, with a failure threshold and an audit log

Four things that only work together: a scheduler that probes, a counter that
decides when a model is dead, an auto-enable that brings it back, and an audit
log that says who did what.

- **The loop probes only models that already carry a verdict**, so a fresh
  install with a hundred-model provider costs nothing until the operator
  presses Test on a model. It disables only on repeated failure, so one
  transient 502 does not take a working model off the listing. And it
  re-enables only what it disabled itself, never what an operator switched
  off by hand — an operator's decision outranks the scheduler's.
- **The threshold counts consecutive failures.** One success resets the
  count, which is what makes a flapping model safe: it never reaches the
  threshold, so it is never switched off. A stored threshold of 1 is raised
  to the minimum rather than honoured, because one error is one transient
  error.
- **Auto-enable is off by default** and only ever re-enables rows carrying
  the scheduler's own mark. A manual enable clears the mark, so from that
  moment the model is the operator's.
- **Audit log** records both scheduler and operator actions in one place, so
  "who took this model off?" is answerable without a second source. Stored
  newest-first and bounded at 200 entries, so a long-running gateway does not
  grow the row without limit.
- **Settings are read every pass, never cached**, so a change saved in the
  dashboard takes effect on the next pass without a restart — the same
  property the auto-ping loop has.

The "failed" probe scope had a cold-start gap that only a live run exposed: a
model with no counter row was skipped forever, so it could never acquire the
first failure that would put it on the watch list, and the scope would only
ever see rows some other path had already written. One probe per new model is
the entry cost, and it is paid once. The unit tests passed with the gap in
place because they seeded counter rows directly.

The route is registered in both route tables. `dashboard/routes.go` is
mounted by tests only, so a route added there alone answers 200 in a unit
test and 404s on the real server — the trap the node-limit, smart-rules and
auto-disable pairs already warn about.

Verification: `go test ./internal/modelhealth/ ./internal/handlers/dashboard/
./internal/handlers/chat/ ./internal/providers/ ./internal/app/` (18 new
cases), `bun test src` (591 pass), `make vet-svelte` (ratchet at baseline 78),
`bun run build`, `golangci-lint` scoped to the diff (0 issues), and a live run
against a gateway with the loop enabled: 89 models across the compatible
nodes were probed, disabled at the threshold, and logged with their reasons.
The disabled lists were cleared and the loop switched off again afterwards.
