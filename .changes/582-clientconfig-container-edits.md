### feat(clientconfig): let a patch replace a container and delete a key

`Patch` could only write a scalar, and only into a key that already existed or
could be appended. That covers `claude`'s six `env` strings and `codex`'s TOML
table, and nothing else: seven of the nine clients in
`docs/specs/client-schema-matrix.md` write an object or an array — `opencode`'s
`provider["9router"]`, `droid`'s `customModels`, `crush`/`pi`/`omp`'s provider
tables — and every `Reset` has to remove a key, which no edit could express.
The adapter task could not start without this.

Two fields on `Edit`, both still splice-only:

- **`ValueJSON`** replaces the target with caller-serialized container text.
  The caller supplies the bytes in the file's own dialect and `Patch` splices
  them; the text is re-parsed before the splice so a malformed value is
  reported as the caller's mistake rather than as bytes this package wrote.
  A scalar edit aimed at a container is still refused: that is almost always a
  caller that meant to update a key inside it, and replacing the object would
  delete the rest of its contents.
- **`Delete`** removes one key and the separator that went with it. Exactly one
  key goes; an emptied parent object stays, because the primitive cannot tell
  leftovers from a value the user wants. Setting `Delete` together with a value
  is refused rather than letting one silently win.

Format specifics, each from a measured constraint rather than a preference:

- **JSONC** picks the separator from the token stream instead of scanning
  bytes, because a comment or a newline sits between members and raw scanning
  picks the wrong comma. A member that opened its own line takes that line's
  terminator with it; one sharing a line takes the gap its comma owned, so
  `{"a": 1, "b": 2}` loses `a` as `{"b": 2}` and not `{ "b": 2}`.
- **TOML** has no container write path on purpose — this package already
  refuses inline tables as unstable, so a TOML table is still written key by
  key with `CreateIfMissing`. A caller that sets `ValueJSON` for TOML is told
  so instead of silently getting a scalar. Delete removes the whole line, since
  the value runs to the end of it.
- **YAML** accepts a flow collection for `ValueJSON`; a block collection that
  spans lines is refused, because replacing only its first line would leave the
  children behind. Delete removes the key's line and, like the JSON path,
  cannot use the old verifier — that one asserts the key is present.

- Evidence: `go test ./internal/clientconfig/ -count=1` passes. Both new
  behaviours were mutation-checked: making delete a no-op reddens 13
  assertions, and removing the scalar-over-container guard reddens the case
  that pins it. Restored after each.
- Not changed: the scalar path, `BackupAndWrite`, `WriteFileAtomic`, and every
  existing test in the package.
