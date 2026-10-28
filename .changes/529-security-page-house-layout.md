### Security page rebuilt on the dashboard's own layout

The Security view still carried the KeiRouter page it was ported from: hand-rolled
cards, inline create form, raw policy JSON, and native `confirm()` dialogs. It now
uses the same primitives as every other page.

- **Layout**: four summary cards, `Card`/`Button`/`Badge`/`Toggle`/`Input`/`Modal`/
  `ConfirmModal` from `web/src/lib/ui`, and the table container used by
  Plans/Combos. Create is a modal; delete and rotate confirm through
  `ConfirmModal` instead of `window.confirm`.
- **Header moved to the top bar**: the page's own header is gone; the top bar now
  shows `Security` with the sidebar's `shield_lock` icon and the page description,
  pulled from `pageMeta`. `security`/`plans` route metadata was added — both tabs
  previously fell back to "Dashboard". Refresh sits with the policy actions.
- **Readable policies**: detectors and action are parsed out of the policy config
  and rendered as badges; subject-scoped rows show their scope id.
- **Every policy is visible**: `GET /api/guardrails/policies` now lists the whole
  scope when `scopeId` is omitted, instead of only policies with an empty scope
  id. A policy bound to one model or key was previously invisible in the
  dashboard even though the resolver applied it. An explicit `scopeId` keeps the
  exact-match read.
- **Creatable subject-scoped policies**: the editor asks for the provider, model,
  chain, or key id, so a non-global policy is no longer stored with an empty
  subject that can never match.
- **Duplicate policy is a 409**: creating a second policy for the same
  (tenant, scope, scope_id) reports that a policy already exists instead of
  surfacing the raw SQLite constraint error.

Verified in the running dashboard: policies render with scope/action badges,
create → list → delete round-trips, a duplicate returns the conflict message, and
the audit table shows direction and severity. `bun run build`, the svelte ratchet
(0 unresolved, 81 baseline), `oxlint`, `go build ./...`, and the db/dashboard
tests pass.
