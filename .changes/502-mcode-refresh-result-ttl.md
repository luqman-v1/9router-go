### fix(oauth): keep a completed minimax-code refresh answer for the upstream TTL

Upstream wraps every provider refresh in `open-sse/services/tokenRefresh/dedup.js`, which has two layers: an in-flight collapse and a result cache that keeps a completed answer valid for 10 seconds (`REFRESH_RESULT_TTL_MS`). The port had only the first.

Those are not the same thing. Once the first flight completes the flight is gone, so a caller arriving just after it posts the same single-use refresh token again and gets `invalid_grant` — which the port classifies as grant-dead, so a working account is signed out rather than retried.

`internal/proxy/oauth/minimax_code.go` now carries the second layer as well. The cache key is upstream's — provider plus the spent token value — so two connections of the same account share the answer while two providers never do. The lookup is re-checked inside the flight, because a caller can arrive between the lookup and the start of the flight.

Three properties come over from upstream, each with a test: a completed answer is reused for 10 seconds, a failure is never cached (so the next attempt reaches the endpoint again rather than replaying a failure for the whole TTL), and the cached answer carries the rotated refresh token so the caller stores the new one rather than resending the spent one.

The cache is bounded as well as expiring. Its key holds a client-supplied refresh token, so a gateway serving many connections would otherwise keep every one of them alive; it holds at most 256 entries, sweeps expired ones on insert, and drops the entry closest to expiry at the ceiling.

This also closes the `internal/proxy/oauth` flake that kept reddening the `Race detector` job on branches that do not touch this package: the test's second caller could land after the first flight completed, and the timing decided whether it landed inside the flight or after it. The sequential case is now covered directly — on the parent the token endpoint is called twice where it must be called once.

The other providers still have no dedup at all, which is a separate change; it is recorded in #501.
