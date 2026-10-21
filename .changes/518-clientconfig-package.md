### feat(clientconfig): write client configuration files without handing the user a broken file

The gateway is the one place that knows how a client is configured, so it is
the place that should be able to configure a client. This package is the
layer that makes that safe: it reads and writes the configuration files of
CLI tools on disk, and it does so without the failure modes that make a
config-writing tool worse than no tool at all.

**Writing is atomic, and the previous version is always recoverable.** A
config is written to a temp file in the same directory, synced, and renamed
over the target; a failure before the rename leaves the original untouched.
The previous content is copied to an exclusive `.bak` file first — never
overwriting an older backup — so a write that goes wrong is still
recoverable by hand. The target's mode is preserved, and a new file is
created owner-only.

**A path that leaves its base directory is refused before any syscall.**
Ancestor symlinks are checked component by component, and a Windows junction
is caught too — `filepath.EvalSymlinks` follows a real symlink but is blind to
a junction, so a link that points outside the base is refused here rather than
discovered later. The final component is refused if it is a link at all:
renaming onto a symlink replaces the link, not its target.

**Comments survive.** JSONC, TOML and YAML are parsed with a token-aware
patcher rather than a regular expression or a full re-marshal, so a user's
comments, key order and formatting are preserved and a URL containing
`https://` is not cut in half. A TOML update addresses one section by name
rather than appending a block, so repeated updates do not produce duplicate
keys.

**A malformed config fails closed.** The file is left exactly as it was, no
backup is taken, and the error names the parse failure. "Repairing" a file
the tool does not understand is how a config-writing tool destroys a
configuration.

**The dry-run counters are in the write path, not beside it.** The counter
wraps the same seam the write path uses — the `rootFS` interface the mutating
operations run on — so "0 disk writes" is a measured fact rather than a
promise. An earlier version wrapped an `*os.Root` the write path never went
through, which made the guarantee unverifiable.

**The connect path refuses what it should.** A gateway URL with userinfo, a
query or a fragment is refused; plaintext HTTP is refused outside loopback
unless the operator says otherwise; a redirect that downgrades HTTPS or
crosses origin is refused, and the redirect target is checked against the
same guard as the first request, so a redirect cannot be used to reach an
address the guard would have blocked. Errors are redacted, so a password or a
userinfo component cannot reach stderr through a `*url.Error`.

**A password is read from a masked prompt, and one that bcrypt would
silently truncate is refused.** `bcrypt.GenerateFromPassword` refuses a
longer input, but `bcrypt.CompareHashAndPassword` — the call the dashboard
makes — silently authenticates a password whose first 72 bytes match the
stored one, so the trailing bytes are decoration. The limit counts bytes, not
characters. A password is also refused on the command line, including the
attached short-flag spelling that no amount of shell-history grepping finds;
the error names the flag and never the value. The prompt clears `ISIG` as
well as `ECHO`, so Ctrl+C lands in the password as a byte instead of killing
the process and leaving the terminal with echo off.
