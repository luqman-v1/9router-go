### fix(opencode): report a closed free tier as unavailable instead of a raw 403

When the OpenCode free-tier gate refuses to serve this traffic, the caller used
to receive the upstream `403 FreeTierError` verbatim — a wall of text telling the
user to download OpenCode, mid tool-call. A 403 reads as "you are not allowed",
so an agent loop treats it as fatal and stops.

The gateway now reports the condition instead of forwarding it:

- **`503` + `Retry-After: 30`** with `type: "free_tier_unavailable"` and a message
  naming the real cause and the real remedies. The status says "temporarily
  unavailable, come back", which is what a closed shared free tier is.
- **`Retry-After` is actually forwarded.** The error-to-response writer copied
  only `Content-Type` and dropped `ue.Header`, so a retry hint was silently
  discarded. It now forwards `Retry-After` and nothing else: copying the whole
  header map leaked the failed response's `Content-Length` and
  `Transfer-Encoding`, which made the reply advertise a body length it did not
  have and truncated it on the wire.
- **A refusal does not cool the connection down.** The gate says nothing about
  the account — the same connection serves the next request once the tier
  reopens — so the cooldown, backoff and model-lock path is skipped and the
  refusal is reported directly. Cooling a healthy account would turn a closure
  into an outage by skipping it for later requests. Scoped to the OpenCode
  providers; every other provider's 403 handling is unchanged.

One attempt only, deliberately. The gate is refusing on *identity*, and the
maintainer's measurement in #367 is that it refuses every identity variant
while closed, so a retry cycle only adds latency to a request that cannot
succeed — and slows the router's advance to the next connection. (An earlier
draft of this PR carried a 5-attempt retry loop with jittered backoff; it was
dropped on review, so that version's fragment text is not carried forward.)

Two related defects were found by testing rather than reading:

- **A truncated error body erased the status.** `doRequestOnce` returned a bare
  `fmt.Errorf` when reading a non-200 body failed, discarding the code, so a
  403 with a mis-declared `Content-Length` became an opaque 502 that no
  classifier — retry, failover or refusal — could recognise. `UpstreamError`
  now carries the status plus a `ReadErr` for the partial read.
- **The first header-copy loop broke an existing test**
  (`TestMiniMaxCodeCreditsRefusalFailsOver`, which asserted a 429 and got a
  truncated body). Caught by the integration suite; fixed by narrowing the
  forward to `Retry-After`.

Verified with `go build ./...`, `go test ./internal/proxy/...
./internal/handlers/chat/ ./internal/translator/ ./internal/providers/`,
`go test -tags=integration ./internal/integration/` (70s), and
`golangci-lint run --new-from-rev=origin/main` (0 issues). The end-to-end cases
drive the real router against a fake gate that refuses: the client sees `503`
with `Retry-After` and no `FreeTierError`, one upstream attempt, and a second
request still reaching the upstream (proving no lock). The hostile case adds a
mis-declared `Content-Length` to exercise the truncated-body path.
