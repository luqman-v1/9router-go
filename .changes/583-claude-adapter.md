### feat(clientconfig): the claude adapter

First of the nine. `~/.claude/settings.json` is JSONC and every value this
writes lives under one "env" object, so the adapter is mostly the tier rule:

```
tier = --<tier> flag || server.claude[<tier>] || leave the existing key alone
```

The third branch is the whole point. The Go gateway does not yet serve the nine
`-settings` routes, so "the server has no value" and "the server answered 404"
are indistinguishable — and deleting on either would wipe a working setup on
every run. A stale value that stays fails softly and visibly; a correct value
that gets deleted fails hard and silently.

Four support pieces the other eight adapters will also need, all added here
rather than duplicated later:

- `resolveGuardOrCreate` makes the client's own directory on first use. Task 1
  refuses a missing base because containment cannot be proven without it, which
  is right for a caller handed a path and wrong for `connect` on a machine where
  the client is not installed yet. Only the adapter's declared base is created,
  and the normal guard runs immediately after, so a symlinked ancestor is still
  refused.
- `emptyDocument` gives a file that does not exist yet a valid starting point.
  Zero bytes is not valid JSONC and leaves TOML with nothing to splice into, so
  without this a first-ever connect fails on exactly the case it exists for.
- `CreateIfMissing` now covers the whole key path, not only the leaf: writing
  `env.ANTHROPIC_BASE_URL` into a settings file with no `env` object is the
  common first run, and refusing it would push the special case into every
  adapter.
- A `Delete` whose key is absent is a no-op rather than an error. Reset runs its
  full list every time, and a file that never held one of the keys is normal.

- Evidence: `go test ./internal/clientconfig/` passes. Covered: round trip back
  to the original bytes with `hasCompletedOnboarding` the one documented
  exception, tier precedence in all four combinations, idempotent second Apply
  (no write, no backup), and a malformed file left byte-identical with no
  backup. Mutation-checked by dropping the server-tier source, which reddens
  five assertions.
