### fix(rtk): keep a JSON document intact and keep the error line through a truncation

Two regressions came in with #485, which made the RTK filter selection command-aware. Both are fixed here.

**A JSON document was corrupted.** The structured-output guard sat after the command matching, so `cat package.json` selected `json-output` by command and the guard never ran. That filter's `includePatterns` keep the structural lines and a few named keys, so every other data line was deleted and the result no longer parsed as JSON. The guard now runs before every selection path, so a command that names a filter does not make the body prose.

Measured through the request path on the same ~900-byte `package.json`: the pre-#485 tree kept 1246 bytes intact, `main` gave 1216 bytes with `"node": ">=20.0.0"` deleted and no longer valid JSON, and this change gives 1246 bytes back byte-identical.

**A diagnostic line was truncated away.** When the command is known and belongs to no filter, `generic-output` is used, its `includePatterns` are empty, and the head/tail truncation drops the middle — which is where a single failure usually sits. A 401-line body with `ERROR failed to connect to database` at line 201 kept that line before #485 and lost it on `main`; it is kept again now.

The diagnostics that survive a truncation are the lines matching the severity vocabulary the filters already share, plus the matched filter's own `preserve.errorPatterns` / `preserve.summaryPatterns`. Those two pattern lists were compiled into the filter and never read by anything; they are wired in now, which is what upstream's `smartTruncate` does for the same purpose. The preserved lines keep their original order, are not duplicated, and are capped at 20 per truncation so the body still shrinks. The truncation marker reports the lines really dropped, so it never claims a line that is still in the body.

Also removes the `matchFilter` bool return that had one caller discarding it and no reader in the tree, and fixes the comment above `isStructuredOutput`, which still claimed `MatchFilter` was called with an empty command.

Verified with `go build ./...`, `go vet ./...`, `go test -count=1 ./...`, `go test -tags=integration ./internal/integration/...`, `gofmt`, and `golangci-lint run --new-from-rev=33ab5f0` (0 issues). The fix-tests fail on the unfixed parent with the two signatures above and pass on this branch.
