### clientconfig: safe file operations and a comment-preserving config patcher

Two layers the client-config writer needs before any of it can touch a user's
real configuration file, neither of which existed:

- **Safe file operations.** `writerAt` now resolves the target under an
  `os.Root` so containment is enforced at the syscall, not by a path check that
  a symlink can win a race against. Backups are exclusive (`O_CREATE|O_EXCL`,
  `<file>.bak.<timestamp>`, nonce retry on collision) so a second write never
  overwrites the first backup. The temp file is created in the target's own
  directory, so the rename stays on one filesystem, and every write is fsynced
  before the rename and the directory after it.

- **Token-aware patcher.** Setting one key in a JSONC, TOML or YAML client
  config rewrites only that key. Comments, key order and formatting survive,
  which full-marshal does not do — and JSONC in particular is a real format, not
  a JSON variant: a parser that strips `//` inside `"https://…"` corrupts the
  file. The TOML path refuses to append a second table that would duplicate a
  key rather than merge into it.

Both are covered by tests that fail if the guard is removed: symlink escape for
a target that does not exist yet, two consecutive backups producing two
distinct files, a disk error mid-write leaving the original intact, a round trip
through each of the three syntaxes, and a patch that must not touch an unrelated
comment.
