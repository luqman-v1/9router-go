### feat(clientconfig): opencode and droid adapters

Both write a container, which is what `ValueJSON` was added for.

**opencode** (`~/.config/opencode/opencode.json`, JSONC) writes its provider
block as one object rather than several key edits. The shape is fixed — npm
package, baseURL, apiKey, one models entry — and writing it whole means the
file's other providers cannot be touched by construction. Every model carries a
`9router/` prefix, stripped before use and re-added on write so a model the user
copied out of opencode does not become `9router/9router/…`.

**droid** (`~/.factory/settings.json`, JSON) rebuilds its whole `customModels`
array. Droid addresses an entry by its `index` field rather than its position,
so inserting one without renumbering leaves every later entry pointing at the
wrong model — and there is no splice that inserts and renumbers at once.

Both resets are ownership-scoped rather than unconditional: opencode only removes
`model`/`agent.explorer.model` while they still carry the prefix, and droid keys
ownership on the `id` marker rather than the model name, so a user whose model
happens to be named the same keeps their entry.

- Evidence: `go test ./internal/clientconfig/` passes. Covered: round trip with
  a sibling provider and a comment preserved, no doubled prefix, a foreign
  `model` left alone by Reset, and droid's array renumbered 0..n on both Apply
  and Reset.
