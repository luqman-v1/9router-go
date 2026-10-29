### Proxy pool: universal edge-relay support for every provider

An edge relay proxy pool (Vercel, Cloudflare Workers, Deno Deploy, Netlify
Functions) is now supported by **every** provider by construction, rather than
by per-provider manual plumbing.

- **The architectural root cause.** A relay is not a CONNECT proxy: it is an
  ordinary HTTPS endpoint that forwards to whichever upstream `x-relay-target`
  and `x-relay-path` name. In this repository that contract was implemented by
  rewriting a provider's `BaseURL` to the relay host and stamping the headers
  into `StaticHeaders`. Every provider that built its own requests had to
  re-apply the contract by hand: four did (`opencode-zen`, `opencode-go`,
  `minimax-code`, `freebuff`), and every other provider failed.
  - **Qoder** signed COSY over the relay host (`403 Signature invalid`) and dialled the relay with no destination (`400 Missing x-relay-target header`).
  - **Trae** appended its session path to the relay host, landing on the relay root.
  - **Zed** and **Windsurf** derived origin from `BaseURL` and dialled the provider directly, leaking the operator's IP.
  - The **embeddings** route (`media.go:157`) used `h.Client` directly, skipping any assigned pool entirely.
- **Relay as a transport, matching upstream.** Upstream expresses relays at
  fetch time — `proxyAwareFetch` takes `proxyOptions.vercelRelayUrl` and rewrites
  the dial while leaving the requested URL untouched. This port now does the
  same via `proxy.RelayTransport` (`internal/proxy/relay_transport.go`):
  `GetClientForConnection` wraps the connection's client with a `RoundTripper`
  that moves the dial to the relay and names the URL the caller actually built.
- **`BaseURL` stays the real provider endpoint.** An executor that signs a URL
  (Qoder COSY) signs the endpoint it actually meant; one that builds a lane path
  (OpenCode, Trae, MiniMax) appends to the real endpoint; and the relay receives
  that exact destination on the wire.
- **Provider-specific relay layers removed.** Hand-rolled relay branches in
  `opencode_zen.go`, `providers.go` (OpenCode Go free and responses lanes),
  `minimax_code.go`, and `freebuff.go` deleted: each was an ad-hoc reimplementation
  of what the transport now does for all of them. `media.go` embeddings route
  switched from `h.Client` to `h.ChatH.GetClientForConnection(connData)`.

Verified live:
- `qd/dfmodel` through a live Vercel relay pool answers `OK` (both streaming and non-streaming).
- `qd/dfmodel` through an HTTP proxy pool answers `OK`, logging `egress=local-http`.
- Provider config for `openai`, `anthropic`, `trae`, `zed`, `qoder`, `opencode`, `freebuff` all keep their real endpoint while carrying `RelayURL`.
- Pinned by `relay_generic_test.go`: an arbitrary request from a provider with zero relay code leaves through the relay addressed to its own URL, query strings survive, and an executor-supplied destination is never overwritten.
