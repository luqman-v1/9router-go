### Model list search, filters and batch selection on the provider detail page

The provider model list was a flat grid: to test five models you clicked Test
five times, to find the vision models you read every row, and to clean up an
import of fifty models you scrolled and unchecked one by one. Upstream has no
search, filter or batch selector on this list at all (`ModelsCard.js`,
`CompatibleModelsSection.js`), so this is a new control surface rather than a
parity port.

- **Search bar** with the same syntax the combo model picker already uses —
  positive terms, `!term` exclusion, quoted phrases and `/regex/` — so an
  operator does not relearn it on a second screen. Matches id and name only:
  the picker's `provider` field is constant within one list and would make
  every row match every query.
- **Filter chips in two labelled rows** (Caps / Status). Capability chips OR
  within their row and AND across rows, so "Vision + Free tier" narrows while
  "Vision" alone does not hide the non-vision rows behind a capability nobody
  asked about. A capability no visible row carries is not offered: on a
  compatible node whose imported models declare no flags, a "Vision" chip
  would empty the list and read as a bug rather than as "none of these have
  it".
- **Status chips** Available / Unavailable / Disabled / Error / Untested /
  Deprecated. "Unavailable" is the union of the reasons a listed model cannot
  serve right now (disabled, failed its probe, or deprecated); "Untested" is
  deliberately not part of it, because a model nobody has probed yet may well
  work and calling it unavailable sends the operator hunting a fault that was
  never observed.
- **Batch selection** over the filtered rows: Test selected, Disable selected,
  Enable selected, Delete selected, plus a select-all header with an
  indeterminate state. Selection is id-based, so checking the errors and then
  clearing the filter keeps the errors checked. Test selected reuses the
  existing bounded sweep (concurrency 6 for the registry, 2 for a compatible
  node, where each probe is a real completion that spends account quota) and
  its provider-switch guard. Delete follows the per-row rule: custom rows are
  removed, registry models are static and are disabled instead, legacy aliases
  lose their alias — one confirm for the batch, and a summary of what landed.
- **Sort** Registry order / A–Z / Z–A. The grid iterates in the toolbar's
  order rather than filtering the registry list through a set, which silently
  ignored the sort.
- **Capability icons on the compatible-node list**, which previously showed
  none. The same model id can be imported into a compatible node, and a row
  that rendered nothing there read as a model with no capabilities at all.

Capabilities now also cover **uncensored models**:

- New `Jailbreak` capability, surfaced as a chip and a row icon. It is not a
  modality and nothing in the request path reads it — it exists so an operator
  can mark the models they know behave this way and filter the list on it,
  instead of recognising them by name.
- Two ways a row gets flagged: the operator's saved flag (the Add Custom Model
  dialog gained a third toggle, and the publisher accepts `jailbreak`,
  `uncensored` or `unrestricted` as the saved name), and an id heuristic that
  matches a hyphen/underscore/dot-delimited segment against the markers
  vendors use (`uncensored`, `unrestricted`, `unfiltered`, `jailbreak`,
  `jailbroken`, `nsfw`, `nofilter`, `no-filter`, `no-moderation`, `no-limits`).
  A thinking suffix is stripped first, and a marker inside a longer segment
  ("unrestrictedly-fast") is not a match.
- `GET /api/models/caps` answers the field for both branches: the static
  registry catalog and the custom rows a compatible node carries. The
  dashboard previously skipped that call for compatible nodes entirely, which
  left every capability chip reading all-false on a custom provider — the one
  place an operator needs them. It now also merges the alias answer, because a
  custom row is stored under the provider's alias (`ds`) and is absent from
  the provider id's (`deepseek`) map.

Verification: `go test ./internal/providers/ ./internal/handlers/dashboard/`,
`bun test` (38 cases for the filter logic), `make vet-svelte` (ratchet holds
at baseline), `bun run build`, `oxlint --deny-warnings`, and a browser pass
over a registered provider and a compatible node covering search, every chip,
select-all indeterminate, batch disable/enable/delete round-trips, and the
Add Custom Model dialog end to end.
