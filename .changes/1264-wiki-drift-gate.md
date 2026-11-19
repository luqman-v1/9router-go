### Added

- The wiki now fails the deploy when it drifts from the code. Three tests in `tools/sitegen` run in the Pages workflow's existing `go test ./tools/sitegen/...` step, before the site is generated, so a wiki that no longer matches the gateway fails the deploy instead of publishing stale docs. `TestWikiMentionsProviders` is a ratchet (`tools/sitegen/wiki-baseline.json`, currently 122) that counts visible registry providers no wiki page mentions by ID or display name — a provider added without a wiki mention fails, and the baseline may only shrink. `TestWikiRoutesExistInCode` fails when `wiki/api.md` documents a route the gateway does not register, with parameter segments normalized so the wiki's per-provider generalizations and chi's wildcards match. `TestWikiProviderCountMatchesRegistry` requires the "N+ providers" claim to be at least the visible registry count.

### Why

Nothing forced the wiki and the code to agree. Since the wiki shipped (PR #908), 267 commits landed on main — 20 PRs, 69 feature commits, 10 new providers — and the wiki still describes 1.9.14: "supports 40+ providers" against a registry of 152 visible, no mention of `opendesign`, `kenari`, `keenable`, `free-pi`, `agy-acp`, `commandcode-goat`, `sensenova-intl`, `synthetic`, `mistral`, or `anthropic`, no `POST /api/cache/semantic/invalidate`, no `GET /api/keys/{id}/budget`, no Gemini `/v1beta` surface. Nothing failed, so nothing got fixed.

### Verification

- `go test ./tools/sitegen/` passes (12 tests, 3 new); `gofmt -l tools/sitegen/` and `go vet ./tools/sitegen/` are clean.
- All three tests were verified against negative controls before landing: a fake provider injected into the registry raises the ratchet count and fails; a fake route in `api.md` fails the route test; an inflated provider count fails the count test. The injected states were reverted.
- The registry parser strips the line comments the generated file places between a map key and its fields — two entries (`sensenova-intl`, `synthetic`) carry them, and without the strip they silently dropped out of the count.
- Release targets still cross-compile with CGO disabled; `go.mod` is unchanged.
