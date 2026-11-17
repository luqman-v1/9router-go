### fix(proxy): honour a dialable pool's noProxy list and validate the proxy scheme

Two gaps left around proxy pools once the dispatch gate learned to dial
`socks5`/`socks5h`: a dialable pool's `noProxy` list was stored and rendered but
never applied, and a proxy URL naming a scheme the transport cannot speak was
installed as a proxy anyway, failing at dial time instead of where the URL was
read.

- **`noProxy` is applied on the dial path**
  (`internal/handlers/chat/connections_proxy.go`). `ShouldBypassNoProxy` was
  called only from `resolveEdgeRelay`, so the bypass list of an HTTP or SOCKS
  pool was ignored: a target the operator explicitly excluded from the proxy
  still went out through it. The list is now read for every dialable pool (and
  for a legacy connection's `connectionNoProxy`) and a matching target keeps the
  connection's direct client. An empty list still proxies everything.
- **The proxy scheme is validated once, in the transport**
  (`internal/proxy/ssrf.go`). `NewSafeTransportForProxy` installed
  `http.ProxyURL` for any URL `url.Parse` accepted, so an undialable scheme
  produced a transport that looked configured and failed only at dial time —
  after the request had committed to that proxy. It now installs the proxy only
  for `http`/`https`/`socks5`/`socks5h` (the schemes Go's transport speaks
  natively) and otherwise falls back to the ambient path, matching the existing
  "cannot parse" behaviour.
- **The pool test endpoint shares that transport**
  (`internal/handlers/dashboard/proxypools.go`). It built its own
  `&http.Transport{Proxy: http.ProxyURL(...)}`, which accepted any parseable
  scheme and dialled without the SSRF dialer's proxy exemption, so it could
  report `passed` for a pool the request path would refuse. It now goes through
  `NewSafeTransportForProxy` via `poolProbeTransport`, and an undialable scheme
  answers `unsupported proxy protocol in <scheme>` rather than a dial error.

Not included: SOCKS4/SOCKS4a. The standard library has no SOCKS4 dialer and
`golang.org/x/net/proxy` is SOCKS5-only, so such a pool is refused with the
message above rather than silently dropped.

Verified with unit tests pinning a pool's `noProxy` decision (listed host,
subdomain, unlisted host, empty list), the dialable-scheme set at the transport
boundary, and the probe endpoint's agreement with it.