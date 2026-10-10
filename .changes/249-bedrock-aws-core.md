### feat(aws): SigV4 signer and AWS credential resolution (Bedrock core, part 1 of 2)

Ports the upstream AWS primitives that Amazon Bedrock needs, without wiring Bedrock
into routing yet. Nothing user-visible changes in this commit: the package has no
callers yet, and the EventStream hardening only tightens a decoder that previously
skipped its integrity checks.

- `internal/proxy/aws/sigv4.go` — Signature Version 4 signing. Hand-rolled rather
  than pulled from the AWS SDK because every signed request here is a single
  query-less POST; the canonicalisation cases that make SigV4 error-prone never
  arise. A URL carrying a query string is refused outright rather than signed
  wrongly. Path canonicalisation double-encodes by default (S3's single-encode rule
  is opt-in), which is what a `…-v1:0` Bedrock model id requires — the single most
  common cause of a Bedrock SignatureDoesNotMatch. The session token is signed as
  well as sent, since omitting it from SignedHeaders is the classic failure for STS
  and SSO identities.
- `internal/proxy/aws/credentials.go` — credential resolution in two modes. Static
  mode reads the keys off the connection; profile mode goes through the AWS SDK's
  shared-config loader, which is what makes `aws sso login --profile X` work.
  Profile results are cached until a refresh lead before expiry, a burst of
  concurrent requests on one profile collapses into a single resolution, and
  failures are never cached so the next attempt retries. The region is validated
  before it reaches a hostname: an unvalidated one would resolve the host to
  `bedrock-runtime.evil.com` and ship the signed request, its body and the session
  token to an attacker-chosen origin.
- `internal/providers/eventstream.go` — hardened. The prelude and message CRCs are now
  verified rather than skipped, the frame cap matches AWS's 24 MiB instead of 10 MiB,
  all ten header value types decode, and every header read is bounds-checked — the
  previous decoder used a bare `break` on a malformed header, which made a corrupt
  frame look like a shorter one. Adds `EncodeEventFrame` so tests and fixtures stay
  in step with the decoder.

Verified with `go test ./internal/proxy/... ./internal/providers/...`. The signer
suite pins AWS's own published SigV4 vector plus a Bedrock-shaped request, so a
canonicalisation regression fails the test rather than reaching production. No AWS
account, no `~/.aws` and no network are required for the suite to pass.
