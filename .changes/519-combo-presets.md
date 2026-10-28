### feat(combos): serve the preset catalog from the provider registry

A combo is a named failover chain, and building one by hand means picking the
models and the order. The dashboard already had a catalog of the chains that
make sense for a given source, but the API had no way to read it — the preset
list and the combo list were two different things, and only one of them could
be created from the other.

`GET /api/combos/presets?source=<source>` now returns the catalog, and
`POST /api/combos/presets` creates the combo the selected preset describes.

Every item is derived, never written out. The Claude presets are whatever the
`cc` registry row in `internal/providers/registry_models.go` currently holds,
plus the Claude Code alias tables; the Cursor presets are the `cu` row. Adding
a model to the registry adds a preset with no second edit, which is also why no
test pins a count.

The dashboard keeps its own copy of the same catalog in
`web/src/lib/models.ts`, maintained by hand. `TestDashboardCatalogListsEveryRoutedModel`
walks every provider the two share and fails when the gateway would route an id
the dashboard omits, so the copy cannot silently fall behind. That check runs one
direction only: models.ts may list ids the gateway does not route.

An unknown or empty `source` is refused rather than answered with an empty
list, so a caller that asked the wrong question is told so instead of being
handed a success it has to interpret.

A preset names its combo after a bare model id, and a combo owns the bare id it
is named after — resolution now looks a stored combo up before the Codex-only
slug inference, matching upstream's order. Without that, the two Cursor presets
carrying a codex slug (`gpt-5.2-codex`, `gpt-5.3-codex`) were served but could
never route through their combo: a request for the bare id went to the `codex`
provider instead.
