### Fixed
- Three OpenCode free ids that no longer route are gone from every static table
  and from the picker. Both live feeds stopped advertising them, and each one
  refuses a real completion:
  - `longcat-2.5-free`: 410 `ModelDeprecated`
  - `inkling-2-small-free`: 410 `ModelDeprecated`
  - `tidepool-alpha-free`: 400 "Upstream request failed: Model is unavailable"

  Probed with `POST https://opencode.ai/zen/v1/chat/completions` and the literal
  key `public`, which is the only evidence `freemodels.go` accepts before an id
  is seeded.

### Why

#1407 added their capability rows and picker entries alongside seven ids that
are still live. The static rows were justified by upstream
`decolua/9router@4e5485836`, but a table row is not evidence a model still
answers: the free lane rotates ids without notice, and an advertised-but-dead id
costs the operator a failed request plus a burned fallback attempt on every
pick. The seven surviving rows stay because both the live feed and a real
completion still agree on them.

This is the resolution the review on #1407 asked for (option 1: drop the ids the
feeds no longer publish, keep the static rows for ids that are still live), done
as a follow-up because #1407 was merged before the question was answered.

### Verified
- `go test ./internal/providers/ -count=1` ok, including the new
  `TestDeadOpenCodeFreeIdsAreNotAdvertised`, which fails if any of the three ids
  regains a capability or token-limit row on `oc`/`opencode`/`opencode-zen`/
  `ocz` (so a later `/models` sync pass cannot quietly re-seed them).
- `TestOpenCodeFreeModelCaps` now covers the seven live ids only, and
  `TestCatalogSweepNoFloorWhereSourceExists` still passes.
- `go vet ./internal/providers/...` clean, `gofmt -l` empty.
- Live probes run today against both feeds and the chat endpoint, recorded
  above: 7 of the 10 ids answer `429 FreeUsageLimitError` (routable, quota
  reached), the other 3 refuse as shown.
- `cd web && bun test src scripts` 1037 pass; `bun run build` OK; the `oc` block
  in `models.ts` drops to 13 entries with no trailing-comma or duplicate defect.
