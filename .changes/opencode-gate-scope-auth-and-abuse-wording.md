### fix(opencode): paid-zen authorization and the network-abuse refusal wording

Three gate-compatibility fixes on the OpenCode lanes, none reaching generic
routing.

- **The paid `opencode-zen` chat lane was sending the public token instead of
  the connection's key.** `zenHeaders` seeds its map from
  `proxy.BuildOpenCodeHeaders`, which hardcodes `Authorization: Bearer public`.
  That map is assigned to `cfg.StaticHeaders`, and `proxy.Forward` applies
  `StaticHeaders` *after* it sets `Authorization: Bearer <apiKey>` — so the
  placeholder overwrote the real credential on every paid zen chat request. A
  paid lane answering as the keyless public client is both a routing bug and a
  credential leak in the log. The free lane keeps the placeholder deliberately:
  the inference gate answers only the public credential.
- **The network-level abuse refusal is now terminal.** The gate words that
  verdict two ways, and only one was matched. `"free models aren't available" …
  "from your current network"` is now matched **conjunctively**: each half
  appears alone in benign prose, so matching either would make an ordinary
  error terminal and suppress a legitimate replay. The two existing phrase
  checks and the allocation-free ASCII fold are unchanged.
- **`classifyOpencodeFreeTierRefusal` reports that wording as
  `free_tier_abuse_detected`** rather than falling through to
  `free_tier_unavailable`, using the same conjunctive rule so the two layers
  agree on what the body means.

Two audit items deliberately **not** ported, recorded here so they are not
re-raised:

- **No session-timestamp re-anchor.** `OPENCODE2API_AUDIT.md` §2.1 asks for
  `TranslateOpenCodeSessionID`'s time half to be anchored to a recent instant,
  on the premise that a digest-derived value decodes as 1970–1972 and the gate
  reads that as staleness. The premise does not hold for this encoding: the
  12-hex prefix reserves 12 bits for the counter and leaves 36 bits of
  milliseconds, and 2^36 ms tops out in **1972** — so *no* id in this layout
  can carry a modern timestamp, including the one the official CLI mints from
  `time.Now()` through the same `descendingTimeHex` call that
  `GenerateOpenCodeSessionID` uses on the measured-working path. Anchoring
  would have changed a working id shape to chase a field that cannot express
  recency. What the derivation must keep is determinism per conversation, which
  it keeps; `opencodeBootNonce` **is** added — a per-process random salt so a
  restart derives fresh sessions instead of resuming a burned quota bucket.
- **No caller-API-key session scope.** §2.1 also asks for the caller's key to
  be mixed into derived sessions so distinct callers never collide.
  `opencodeStableSessionKey` already scopes by connection **and** egress proxy,
  which prevents that collision; adding key scope would fragment the
  per-session quota budget the free tier accounts, turning one conversation's
  allowance into several smaller ones.

`x-opencode-project` stays at the single `"global"` placeholder. Minting a
per-request real-looking project id is a deliberate non-goal for this project,
not an oversight: it changes which upstream quota bucket a request is accounted
to, which is gate circumvention rather than a lane bug fix. The related
cleanup — a caller-supplied `x-opencode-project` continues to win over the
placeholder — is unchanged and remains the supported way to scope a deployment.

Verified: `go test ./internal/proxy/... ./internal/translator/... ./internal/handlers/chat/...`
green (the three `internal/proxy/executor/mcodelease` failures are pre-existing
on `main` and unrelated), `go build ./...` clean. New tests pin the per-lane
`Authorization` on both zen lanes with an end-to-end paid chat assertion, the
conjunctive abuse pair including both half-matches as non-terminal, and the
session-derivation invariants (stability within a process, distinctness across
conversations, canonical ids forwarded verbatim, the placeholder project value).
The paid-zen test was confirmed to fail against the unfixed code.
