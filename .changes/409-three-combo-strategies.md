### Changed

- Combo strategies are aligned with JRouters: exactly three are selectable —
  `round-robin` (rotate the starting member), `fallback` (walk the members
  in order), and `fusion` (panel + judge; JRouters' ensemble). New
  `db.NormalizeComboStrategy` folds every legacy value into the closest of
  the three, mirroring JRouters' `NormalizeComboKind`: failover, the global
  mode's `first-model`, and capacity resolve to fallback (capacity's walk in
  this port is ordered; its adapter pools are a separate settings feature
  and keep working); ensemble resolves to fusion; sticky — rotation with a
  window, the same engine block as round-robin — and load-balance, smart,
  weighted, race, parallel, empty and unknown values resolve to round-robin.
- The default strategy for a combo with no per-combo override and no global
  mode is now round-robin (JRouters' default), not fallback. Resolution
  (`resolveComboStrategy`) normalizes stored values, `SetComboStrategy`
  persists the canonical name (and no longer deletes the entry when the
  chosen strategy is the old default — an explicit fallback choice now
  sticks, the backend half of the upstream #4576 trap), and the dashboard
  combo create/update writes normalize too.
- The engine (`applyComboStrategy`) treats an unrecognised value as
  round-robin instead of an ordered walk, keeps fallback/capacity ordered,
  and keeps fusion's panel in the configured order. Round-robin's walk is
  unchanged: it still advances member to member on member-level failures
  with account locks only for account-scoped failures (#407).
- Dashboard: the combo card's strategy select offers only Inherit +
  Round Robin / Fallback / Fusion, with the same legacy coercion as the
  server; the global Combo Routing Mode select offers the same three
  (storing canonical names, showing `first-model` as Fallback).

### Verified

- `go test ./internal/db/ ./internal/handlers/chat/` — all pass, including
  new `TestNormalizeComboStrategy` / `TestResolveComboStrategy` tables and
  engine tests that an unknown strategy rotates and fusion keeps its panel
  order.
- Web build passes; the served bundle carries the three-option select and
  none of the removed labels. (The card's bun:test suite was updated but
  cannot run here — no bun on this machine.)
- Binary rebuilt and installed; gateway healthy on :20130. Combos already
  stored with a canonical strategy (e.g. round-robin) are unaffected; a
  combo with no strategy anywhere now rotates instead of walking in order,
  and can be pinned back by choosing Fallback on its card.
