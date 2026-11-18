### fix(opencode): harden the free-tier lanes and add gate probes

Five ported fixes from a comparison audit against an external reference
implementation (`jasonxu114514/opencode2api`), all scoped to the OpenCode lanes
and none reaching generic routing. Four audit proposals were deliberately not
ported; all are recorded at the end with their reasons so they are not
re-raised.

**The paid `opencode-zen` chat lane was sending the public token instead of the
connection's key.** `zenHeaders` seeds its map from `proxy.BuildOpenCodeHeaders`,
which hardcodes `Authorization: Bearer public`. That map is assigned to
`cfg.StaticHeaders`, and `proxy.Forward` applies `StaticHeaders` *after* it sets
`Authorization: Bearer <apiKey>` — so the placeholder overwrote the real
credential on every paid zen chat request. A paid lane authenticating as the
keyless public client is both a routing bug and a credential leak in the log.
The free lane keeps the placeholder deliberately: the inference gate answers only
the public credential.

**Network-level abuse refusals are terminal again.** The gate words that verdict
two ways, and only one was matched, so a terminal egress ban was replayed once
and its penalty extended. `"free models aren't available" …
"from your current network"` is now matched **conjunctively**: each half appears
alone in benign prose, so matching either would make an ordinary error terminal
and suppress a legitimate replay. Applied in both the retry classifier and the
handler's refusal classifier, so the two layers agree on what a body means.

**Title-generation requests no longer get agent shaping.** OpenCode 1.x/2.x fires
tool-less title-generation calls. The gate accepts a tool-less title call and
refuses the same call once agent tool declarations are attached, so shaping one
like an agent turn was itself the cause of the refusal — after which the router
spent an entire proxy-pool walk retrying a request that should never have carried
tools. `IsOpenCodeTitleRequest` detects the shape (at most three turns, no tool
declaration in any spelling, no assistant or tool turn, **and** a known title
opener) and `ShapeOpenCodeV2TitleRequest` keeps the harness preamble the gate
fingerprints while installing no tools. All six free-lane call sites route
through one decision point (`shapeOpenCodeFreeBody`), which has its own test
because a site left on the agent shaper would keep passing the translator tests
while breaking the behaviour they exist for. The opener requirement is what makes
detection safe to ship: a short tool-less turn is a perfectly ordinary request,
so the failure mode has to be "behaves exactly as today" rather than "a real turn
silently loses its tools".

**The first SSE frame is read before `200 OK` is committed.** The gate can answer
a refusal inside an HTTP `200` SSE frame, or stall before its first frame, and
once the response head is on the wire no failover is possible. Measured against
the unfixed code, an in-band refusal came back as a generic **502**: the gate's
actual verdict was lost, so `classifyOpencodeFreeTierRefusal` classified on the
wrong code and `parkRefusedProxyWithPool` parked the egress with the wrong
window. A `FreeUsageLimitError` frame now surfaces as **429** and a
`FreeTierError` frame as **403**. Built on the existing
`proxy.NewStallReaderWithFirstEvent` rather than a second timeout implementation,
so a stalled upstream is *closed*, which is what unblocks the pending read. The
prelude is capped at `constants.UpstreamErrLimit`; an oversized prelude is itself
a refusal. Guarded on `isEventStreamResponse`, so a lane that answers a streamed
request with one JSON document passes through untouched — without that guard the
gate rejected valid JSON `200`s. Wired on all six free-lane response paths.

**The reference Bun transport and v2 Chat body key order are implemented but
default-off** behind `OPENCODE_V2_REFERENCE_FINGERPRINT=1`. The reference
ClientHello (HTTP/1.1 ALPN only, `X25519MLKEM768` first, both key shares,
`PKCS1WithSHA1` appended) could not be validated: the free tier answered every
probe with `429 FreeUsageLimitError`, so no reference layout could be shown to
return `200`, and a wrong ClientHello `403`s *every* free-tier request. One flag
flips and one flag reverts the spec, `WithForceHttp1` and the Chat body key order
together. Both layouts keep 17 ciphers and 13 extensions, pinned by test, so the
flag cannot silently move the two things the gate is most likely to measure. A
latent bug in that area came out while wiring it: the stealth client cache was
keyed on proxy URL alone, so a client built under one fingerprint was returned
for the rest of the process lifetime and the flag would have been inert.

**Not ported, recorded so it is not re-raised:**

- **No session-timestamp re-anchor.** The audit asks for
  `TranslateOpenCodeSessionID`'s time half to be anchored near `now`, on the
  premise that a digest-derived value decodes as 1970–1972 and the gate reads
  that as staleness. The premise does not hold for this encoding: the 12-hex
  `ses_` prefix reserves 12 bits for the counter and leaves **36 bits of
  milliseconds**, and `2^36` ms tops out in **1972** — so *no* id in this layout
  can carry a modern instant, including the one the official CLI mints from
  `time.Now()` through the same `descendingTimeHex` that
  `GenerateOpenCodeSessionID` uses on the measured-working path. What the
  derivation must keep is determinism per conversation, which it keeps, plus a
  per-process `opencodeBootNonce` so a restart derives fresh sessions instead of
  resuming a burned quota bucket.
- **No caller-API-key session scope.** The audit also asks for the caller's key
  to be mixed into derived sessions so distinct callers never collide.
  `opencodeStableSessionKey` already scopes by connection **and** egress proxy,
  which prevents that collision; adding key scope would fragment the per-session
  quota budget the free tier accounts, turning one conversation's allowance into
  several smaller ones.
- **`x-opencode-project` stays the single `"global"` placeholder.** Minting a
  per-request real-looking project id changes which upstream quota bucket a
  request is accounted to, so it is out of scope by design rather than by
  oversight. A caller-supplied `x-opencode-project` continues to win over the
  placeholder, which remains the supported way to scope a deployment.
- **No tool-cloaking strategy change.** The audit's `mcp_<digest>` proposal is
  unproven here: the two reference projects measured opposite results, and this
  host cannot settle it because the gate answers the quota refusal **before** it
  validates tool declarations (a missing `model` returns `400` first, but a
  foreign tool name returns the same `429` as a tool-less body).
  `TestLiveOpenCode_ToolCloakingProbe` (integration tag, opt-in via
  `9ROUTER_LIVE_OPENCODE=1`) runs the `no_tools` / `raw` / `mcp_<digest>` matrix
  and reports rather than asserting, so the question stays answerable from a
  clean egress.

Verified: `go build ./...` clean; `go test ./internal/proxy/...
./internal/translator/... ./internal/handlers/chat/...` green (the three
`internal/proxy/executor/mcodelease` failures are pre-existing on `main`,
reproduced by checking out upstream and re-running);
`go test -tags=integration ./internal/integration/` green; `go vet` clean;
`go.mod` unchanged, so the `CROSS_TARGETS` portability gate is not engaged. Both
substantive fixes were confirmed to fail against the unfixed code — the paid-zen
test reported `Authorization = "Bearer public"` and the first-event test reported
`status = 502, want 429/403`. Tests pin the per-lane `Authorization`, the
conjunctive abuse pair with both half-matches as non-terminal, 17 title-detection
cases including all four false-positive guards, the shaping contract, the
six-site wiring, the byte-exact healthy-stream replay, both in-band tokens
mapping to their status, EOF before and mid frame, the oversized prelude, a
stalled upstream bounded by the deadline, context cancellation releasing the
attempt, both transport layouts' curve/share/sigalg/ALPN facts, the unchanged
cipher and extension counts, the flag's exact-value parsing, the cache-key
separation, and both body orders including their determinism.
