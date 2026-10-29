### feat(clientconfig): the codex adapter, and empty TOML tables

`~/.codex/config.toml` is TOML, so every write goes through the key-by-key path
with `CreateIfMissing` — the first edit appends `[model_providers.9router]` and
the rest land inside it. `auth.json` beside it is deliberately not a target: a
custom provider ignores it and reads the static `Authorization` header instead,
which is what the adapter writes.

Two behaviours worth naming:

- The legacy `agents.subagent` key is deleted by **Apply**, not only by Reset.
  Codex has a newer `default_subagent_model`, and a config holding both gives it
  two answers for one question.
- Reset only removes `model` and `model_provider` when they still name us. Once
  the provider table is gone they are the user's own keys, and deleting a
  provider selection someone changed by hand is the loud kind of wrong.

Clearing a TOML table needed a fix in the patch layer, not the adapter. Deleting
the last key under `[model_providers.9router]` left the bare header behind, and
a nested table's header is written under its parent's, so emptying the child
left the parent empty too. `removeTOMLLine` now drops a header once nothing is
left under it and walks up while each parent is still empty, stopping at the
first table that still holds the user's keys.

- Evidence: `go test ./internal/clientconfig/` passes. Covered: round trip
  preserving the user's comment and unrelated keys, three Applies leaving exactly
  one table header and one `Authorization` line (Node's writer stacked them),
  Reset leaving a foreign `model_provider` alone, the legacy key dropped, and a
  malformed file untouched with no backup.
