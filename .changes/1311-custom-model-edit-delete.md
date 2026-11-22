### Added — Edit custom models; delete no longer parks them under "Disabled models"

- Every custom model row on a provider page now has an Edit button that
  reopens the Add Custom Model modal in edit mode (id and type locked,
  fields prefilled, extraBody pretty-printed). Save re-posts the full stored
  row under the same key, so `name` and other fields survive; blanking the
  request body field sends `extraBody: null`, which clears it. Compatible-node
  rows use a separate list and are not editable this way.
- Fix: deleting a custom model (row X, trash, or "Delete selected") now removes
  it instead of moving it to "Disabled models"; the id the delete endpoint
  parks in `disabledModels` is pruned. Built-in models keep disable semantics.
  Pre-existing (X button since 4f4b4aeaf, endpoint disable-append in 13cce967).
