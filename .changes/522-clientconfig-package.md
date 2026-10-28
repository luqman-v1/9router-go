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
query or a fragment is refused; plaintext HTTP to a public host is refused
outright, and `--allow-insecure` relaxes that rule only for a loopback
destination, never for a public one. A redirect that downgrades HTTPS is
refused; one that crosses origin is refused only when the request carries a
credential, in a header or in the body — a credential-free GET is allowed to
follow a cross-origin redirect. Every redirect target is checked against the
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

**Cost, measured, and where it stops.** `Patch` is linear in file size for
JSONC and YAML: a 512 KB sample went from 987 ms to 15 ms once the JSONC
duplicate-key check stopped rescanning every member of an object. TOML is not
linear, and cannot be made so from here: `go-toml/v2`'s duplicate detection
walks the sibling keys of a table on every insert, so a 512 KB TOML document
takes about a second to parse — 25 ms at 64 KB. `Patch` pays that parse once,
not twice; the output is checked with the linear parser plus a read-back of the
edited key, instead of a second full semantic pass.

The linearity claim is per-format and shape-dependent, so it is measured as a
ratio rather than asserted: doubling the sample costs about 2x for JSONC and
YAML (2.1x and 1.9x from 256 KB to 512 KB, `TestPatchScalesLinearly`) and about
4x for TOML (3.8x over the same span), on one flat table of N scalar keys — the
shape that puts every key in the same sibling set, and so the worst case for
`go-toml`'s seen-tracker. `TestPatchTOMLAddsNoPass` pins the part this package
controls: Patch spends 1.0–1.1x the cost of a bare `toml.Unmarshal` on the same
bytes, so the quadratic term is the dependency's, not a second pass of ours.

A caller writing a config should treat `LargeFileWarningBytes` as the point to
ask the user, because both parsers build a tree above the whole file — a 256 KB
TOML patch allocates about 33 MB, 121x the input, and 1 MiB about 140 MB.

**No caller yet.** Nothing outside this package imports it, and the write
functions take an unexported `guard` with no exported constructor, so they
cannot be reached from outside at all. This change brings the package in with
its own tests; wiring it into the CLI is a separate change, and nothing here
promises a user-visible behaviour until that lands.
