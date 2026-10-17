### opencode-zen: stop injecting tool stubs the legacy gate never asked for

`ConcealFingerprintTools` renamed the caller's capitalised quartet tools
(`Bash`, `Glob`, `Grep`, `Read`) to their canonical lowercase spelling and then
appended every *absent* member as a no-op stub. The renaming is load-bearing —
a harness-shaped body is rejected with 403 FreeTierError unless the names match
(measured 2026-09-18) — but the injection was broader than the gate requires.

Probing against the legacy `/zen/v1` free tier (reported in the opencode2api
`a9470df` change) shows a single tool named `read` is sufficient: `[read]` alone
passes, while `bash`/`shell`/`edit`/`glob`/`grep` alone — or any combination
without `read` — answer 403, and neither the description nor the parameter
schema of a declared tool is validated on that lane. The three extra stubs were
therefore payload on every paid `opencode-zen` / `opencode-go` request, and they
offered the model tools that do nothing.

Injection is now driven by a separate `openCodeRequiredTools` list containing
just `read`, while `OpenCodeFingerprintTools` keeps the full quartet for
*collapsing* — renaming a client-sent `Bash` to `bash` is what makes the body
acceptable, so that behaviour is unchanged. A body that already declares the
required tool still gets nothing added, and the `/inference` lane
(`ConcealOpenCodeV2FingerprintTools`) is untouched.

Deliberately preserved: paid lanes keep the quartet identity, and a client tool
already named exactly `read` keeps its own description and schema.

Verified with `go test -count=1 ./internal/translator/ ./internal/proxy/...
./internal/handlers/chat/`, with the injection tests tightened to assert the
exact remaining tool set rather than a count.
