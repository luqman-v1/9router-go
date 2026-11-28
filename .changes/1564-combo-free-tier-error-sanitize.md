### fix(chat): classify the OpenCode free-tier 429 on the combo path

A combo's first member is usually a free lane (`combo-wombo` starts with
`oc/hy4-flash-free`). When that egress reported `FreeUsageLimitError`, the
combo loop wrote the gate's raw 429 body to the client and to the request
details ledger: "subscribe to OpenCode Go", an instruction the caller cannot
act on and that an agent loop misreads as a credential problem. The
single-provider failover path already classified this shape, but the two combo
loops (chat completions and messages) bypassed that classifier.

Both loops now pass a terminal free-tier refusal through
`opencodeClosureError`, the same gate classifier the single-provider path
uses: a classified 429 (`free_tier_quota_exhausted`) with a `Retry-After`
instead of the upstream subscribe prompt. Non-OpenCode errors and genuine
rate limits still pass through untouched.

Parity source: upstream `decolua/9router` `master` commit `989b1eec`
(`fix(opencode): retry free-lane overload 429 and drop rejected sessions`)
makes the executor discard a spent session instead of pinning it; this
fragment only fixes the Go gateway's client-facing classification, which is
local to `internal/handlers/chat`, and records the divergence.

Verified: `go test ./internal/handlers/chat/...` with a new regression test
that drives both combo endpoints against a fake upstream returning the real
`FreeUsageLimitError` body.
