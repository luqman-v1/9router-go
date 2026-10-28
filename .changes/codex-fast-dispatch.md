### chat: wire the Codex Fast preference into dispatch

The mode was applied by a helper nothing invoked: the account-level preference
was read, the body rewritten, and the result discarded. It is now wired into the
one place that owns the outbound body (`fallback.go`), after every other
rewrite, so a token saver or an image prefetch cannot reorder or drop the field.

The precedence is:

1. An explicit `service_tier` in the body wins. `"fast"` becomes `"priority"`;
   any other value is left alone and blocks the account default.
2. A `speed` field, whatever its value, blocks the account default but never
   promotes. `speed` is a client hint, not a Codex service tier —
   `executor.normalizeCodexServiceTier` drops it and
   `internal/integration/codex_service_tier_test.go` pins that behaviour.
3. With no intent in the body, the account's stored `codexFastMode` preference
   decides.

The rewrite copies the body it was given and never mutates the caller's bytes,
so a retry that runs the loop again starts from the same request and one
account's toggle cannot leak into the next account's attempt.
