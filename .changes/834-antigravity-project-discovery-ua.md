### Antigravity project discovery sends the client's own identity

The onboarding RPCs (`loadCodeAssist`, `onboardUser`) identified as the CLI
shape under a constant named `antigravityIDEUserAgent` — the name and the value
disagreed, and neither was the identity the connection had signed in as.

The value is load-bearing, not cosmetic. Google answers the generic
`google-api-nodejs-client` identity with a 200 that **omits**
`cloudaicompanionProject` even for a fully provisioned account, so a caller
sending it concludes "no project" and every chat then returns 502 for a healthy
login. Verified live on one token: the generic UA yields no project, while the
Antigravity identity returns the real project id.

`antigravityProjectUserAgent` now resolves the profile from the connection's
`providerSpecificData.clientProfile`, so an account signed in as the CLI is
discovered as the CLI rather than as the IDE. The chat lane knows only the
connection id, so `antigravityConnData` reads the row to resolve it; the media
lanes already hold the blob and pass it directly.

Two fallbacks are deliberate. A caller with no connection data (the media lanes,
which only carry a token) gets the CLI identity — the shape the Gemini 4 Argon
lanes answer. And an **unrecognised** setting also falls back to CLI rather than
to the normaliser's `ide` default: an unknown value must not silently change
which client the gateway claims to be, because the answer depends on it.

This also puts `providers.AntigravityUserAgentFor` (added earlier) to use — it
existed but nothing called it.

Verified: `go build ./...` clean; `go test ./internal/handlers/chat/
./internal/handlers/media/ ./internal/providers/` pass. New tests pin the
identity per profile including the legacy `harness`/`sdk` spellings, the
unrecognised-value fallback, and that the resolved identity actually reaches the
wire rather than only the unit-level helper.
