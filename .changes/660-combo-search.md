### feat(dashboard): search box for the combo list

upstream decolua/9router 9b4ea9eb parity (#629). The combo list takes a
case-insensitive search over the combo name and its member model ids, so both
"deepseek" and "deepseek-v4.1-flash" find the right rows. An `X of Y shown`
counter appears while a query is active, the input carries an inline clear
action, and a no-match state names the query and offers its own clear button.

Select-all follows the search: with a query active it reaches only the rows on
screen, so a bulk delete cannot take out hidden combos — the foot-gun the
upstream commit fixed. A selection that survives a cleared search still applies
to the full set.

Verified: `bun run build`, the svelte-check ratchet at its pinned baseline with
no unresolved identifiers, `oxlint --deny-warnings`, `go test ./web/...`, and a
click-through on a live gateway against three seeded combos (filter by name,
filter by member id, filtered select-all reaching exactly the visible row,
selection surviving a cleared search, empty state restoring the full list).
