### feat(opencode): opt-in reference Bun fingerprint and v2 Chat body key order

The reference implementation (`opencode2api`) reproduces a newer Bun fingerprint
than this gateway sends. Porting it **on by default** would have meant replacing
a handshake that runs in production with one that could not be validated: the
probe egress's free allowance was spent, so no request could be driven to a
`200` (see the first-event entry). The layout is therefore implemented and
tested but **off unless `OPENCODE_V2_REFERENCE_FINGERPRINT=1`**, so the default
lane sends the exact bytes it always has and a single flag (or a single revert)
flips the whole decision.

What the flag changes:

- **ClientHello** (`bunBoringSSLSpecFor`): ALPN drops to `http/1.1` only (Bun's
  `fetch` does not speak h2); `SupportedCurves` leads with the post-quantum
  hybrid `X25519MLKEM768` and drops `CurveP521`; both `X25519MLKEM768` and
  `X25519` key shares are sent; `PKCS1WithSHA1` is appended last. Cipher list and
  extension count are unchanged in both layouts, and a test pins that, so
  flipping the flag cannot silently move either.
- **Transport**: `tls_client.WithForceHttp1()` is added under the flag, so the
  transport cannot negotiate h2 even if a server ignored the ALPN list.
- **Chat body key order** (`marshalOpenCodeV2Chat`): the leading block
  `model, messages, tools, stream, stream_options, max_completion_tokens` is
  written first, then the remaining keys alphabetically. It is still fully
  deterministic — a fixed head plus an alphabetical tail — which is the property
  `marshalStable` exists for, so prefix caching is unaffected.

Two implementation details worth recording because the first attempt got them
wrong:

- **The leading order cannot go through the map marshaler.** `json.Deterministic(true)`
  sorts members, so handing it a map whose insertion order encodes the
  requirement silently discards it. The keys are written one at a time; a test
  asserts the emitted order, which is what caught this.
- **A stale stealth-client cache would have made the flag inert.**
  `getOpencodeStealthClient` keyed its cache on proxy URL alone, so a client
  built under one fingerprint was returned for the rest of the process lifetime
  regardless of the flag. The cache key now folds the flag in.

**Open question settled.** Whether an explicit `KeyShareExtension` carrying
`X25519MLKEM768` produces usable key material was unknown: the extension
overwrites the shares utls generated from `SupportedCurves[0]`, and its entry
carries no bytes. `TestBunSpec_MLKEMKeyShareIsMaterialized` builds the spec and
inspects the result — **1216 bytes for the hybrid share and 32 for X25519** — so
listing the group is sufficient and no hand-rolled key material is needed.

**Deliberately not ported, with reasons:**

- **`Accept-Encoding: gzip, deflate, br, zstd`.** `tls-client` decompresses gzip
  only (`TransportOptions.DisableCompression` drives Go's gzip-only handling);
  brotli and zstd would arrive as opaque bytes and break the SSE scan. Sending a
  codec the client cannot decode is worse than a fingerprint mismatch, because
  it looks like a provider fault. Gzip stays as-is.
- **Dropping `b3` / `traceparent`.** The reference sends neither, but this
  gateway's synthetic trace headers are unverifiable against a live gate right
  now, and removing them is a fingerprint change like any other. Left in place
  rather than changed blind.
- **Dynamic prompt/env metadata.** `Today's date`, model name and the `<env>`
  block stay frozen at the captured values. The gate fingerprints this prefix
  byte-for-byte, a dynamic date also moves the prompt prefix every day (breaking
  prefix caching), and neither effect could be measured. Revisit with a clean
  egress before touching the one string the gate is known to compare.

Verified: `go test ./internal/...` (failures limited to `browserlogin`,
`daemon`, `proc` and `mcodelease`, all reproduced on a clean `main` and
environment-specific), `go test -tags=integration ./internal/integration/` green,
`go build ./...` clean, `go.mod`/`go.sum` unchanged so the `CROSS_TARGETS`
portability gate is not engaged. Tests pin both layouts' curve/share/sigalg/ALPN
facts, the unchanged cipher and extension counts, the flag's exact-value parsing,
the cache-key separation, and both body orders including their determinism.
