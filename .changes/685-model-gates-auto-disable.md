### Model gates for custom providers, and auto-disable dead models

Four things on the provider model list.

- **Status filters and batch toggles on custom providers.** The
  compatible-node branch rendered no status chips and no disable/enable
  buttons, so an operator managing an imported list of fifty models had
  "Test all" and "Remove all" but no way to switch one off without deleting
  it. Both branches now share the same toolbar: the status chips, the
  per-row visibility toggle, and the batch Disable/Enable selected actions.
- **Tests proving the gate already covered compatible nodes.** The
  disabled-models filter keys its KV scope by both the stored alias and its
  canonical provider; for a compatible node the alias resolution collapses
  all three spellings onto the node id, so the existing check covered it.
  Two tests pin that — one keyed by node id, one keyed by the connection's
  prefix — because the failure mode (the gate silently stops hiding a node's
  models) is invisible.
- **Auto-disable dead models.** `POST /api/models/auto-disable` folds a
  caller-supplied list of dead ids into the provider's disabled list. The
  merge is server-side on purpose: the dashboard already holds the verdicts
  from its own probes, and sending only the failures avoids a
  read-modify-write from the browser, which is how two open tabs disable
  each other's models. `dryRun` previews without writing, an id that is
  already off is reported separately rather than silently dropped, and a
  call with nothing new is a no-op that does not reorder the row. "Blocked"
  counts as dead — the account is parked and the model cannot serve until
  the cooldown lifts — but a model nobody has probed is never included.
- **The route is registered in both route tables.** `dashboard/routes.go`
  is mounted by tests only, so a route added there alone answers 200 in a
  unit test and 404s on the real server — the trap the node-limit and
  smart-rules pairs already warn about.

Verification: `go test ./internal/handlers/chat/ ./internal/handlers/dashboard/
./internal/providers/` (7 new cases), `bun test src` (591 pass), `make
vet-svelte` (ratchet at baseline 78), `bun run build`, `oxlint
--deny-warnings`, and a browser pass over a registered provider and a
compatible node covering the status chips, the per-row toggle, the batch
round-trip, and the auto-disable endpoint end to end — including confirming
the disabled model leaves `/v1/models` while its siblings stay.
