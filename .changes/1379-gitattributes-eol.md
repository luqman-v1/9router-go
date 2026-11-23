### chore: pin line endings with .gitattributes and renormalise the four CRLF files

The repository had no `.gitattributes`, so Git stored whatever line ending the
writing machine produced. Five text files ended up CRLF at HEAD while their
neighbours in the same directory are LF — `web/src/api/client.ts` at 3042 lines,
`web/src/App.svelte` at 768, `web/src/components/combos/types.ts` at 852,
`tools/sitegen/wiki-baseline.json`, and `CHANGELOG.md`.

Nothing in the tree declared which was intended, so the only way to tell them
apart was to open the blob. With `core.autocrlf=false` the cost is paid on every
touch rather than once: `internal/handlers/dashboard/jailbreak_sync_test.go`,
normalised to LF by hand for #1362, showed as 659 insertions and 659 deletions to
say nothing at all.

- **`.gitattributes`** declares `* text=auto eol=lf`, names the text types where
  a wrong guess is expensive, and marks the binaries explicitly. Two entries are
  the reason the file has to name extensions at all rather than trusting
  `text=auto`: `*.pgo` is gzip — `cmd/9router-go/default.pgo` is a compressed PGO
  profile, not a Go source file, so a text rule on it corrupts the profile — and
  the font/icon formats would otherwise be sniffed as text and turn a byte
  sequence into a permanently damaged diff.
- **Four files renormalised** to LF, content unchanged. Proven rather than
  assumed: each blob's SHA256 before and after is identical once `\r` is
  stripped, while the raw blob hash changes — endings moved, nothing else did.
  `git diff` against the new blobs is empty, so a Linux checkout reads clean.
- **`CHANGELOG.md` is deliberately left CRLF**, marked `-text` so it stays
  byte-identical in the blob and on every checkout. `make changelog-merge`
  rewrites the whole file at each release anyway, so a conversion buys nothing,
  and a `CHANGELOG.md` hunk is not allowed in a PR diff (§7.6). Without `-text`
  the blanket rule stages it, which is how the first attempt at this change went
  wrong.

Verified: `go build ./...` clean, `go vet` clean, `go test` green on
`internal/db`, `internal/handlers/dashboard` and `tools/sitegen` (the consumer of
the converted JSON), `bun run build` clean, `oxlint` clean, 979 frontend unit
tests pass, and `golangci-lint --new-from-rev` reports nothing on the diff.
`gofmt -l` still lists `internal/integration/key_model_access_test.go` and
`internal/integration/opencode_bridge_done_boundary_test.go`, both untouched
here and pre-existing.