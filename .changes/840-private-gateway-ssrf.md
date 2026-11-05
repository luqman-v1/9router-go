### fix(proxy): per-connection opt-out for private-network dials (SSRF guard)

- A connection pointed at a local gateway (a second 9router on `127.0.0.x`, an
  Ollama-style daemon, a LAN peer) was refused by the SSRF guard's socket-level
  `ControlContext`, surfacing as `502: request to private/reserved IP blocked by
  SSRF protection` (issue #840). Unlike upstream, which never guards chat
  upstreams, 9router-go's dial guard is a deliberate hardening — so the fix is
  an opt-in, not a removal.
- Adding `allowPrivateNetworking: true` to a connection's data (persisted via
  POST/PUT `/api/connections`, read back through the sanitize allowlist, and
  plumbed to chat (`internal/handlers/chat/fallback.go:tryForwardWithConnection`)
  and the connection probe (`connection_probe.go:testSingleConnection`))
  lifts the guard for that connection only, riding
  the request context (`proxy.WithAllowPrivateNetworking`) so both the plain
  forwarder and every executor apply it at dial time. The process-wide
  `ALLOW_PRIVATE_NETWORKING` escape hatch is unchanged and still required for
  anything that is not a connection (media, search, etc.).
- Verified with two end-to-end chat handler tests on a loopback httptest
  upstream with `ALLOW_PRIVATE_NETWORKING=false`: the same connection is
  refused without the flag and served with it. Additionally
  `TestNewSafeDialer_PermitsPrivateViaContextOptIn` pins the dial-guard seam,
  and `TestHandleCreateConnection_PersistsAllowPrivateNetworking` /
  `TestHandleUpdateConnection_AllowPrivateNetworkingPersistence` pin the
  dashboard create/update persistence (toggle on/off round-trip).
