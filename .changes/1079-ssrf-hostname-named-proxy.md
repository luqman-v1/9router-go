### fix(proxy): a hostname-named egress proxy is exempt from the SSRF guard by resolution

- **A proxy named by hostname was always refused when it resolved into a
  reserved range.** An install whose connections egress through
  `connectionProxyUrl: http://hatch-egress-proxy:3128` — a Podman-internal
  proxy that resolves to `198.19.0.1`, inside the RFC 2544 range the dial
  guard blocks — saw every upstream call fail with
  `proxyconnect tcp: dial tcp 198.19.0.1:3128: request to private/reserved IP
  blocked by SSRF protection` and, under `strictProxy`, no way out at all
  (#1078). The same setup worked on 1.9.14: that version's private-IP
  predicate did not yet know the RFC 2544 range. The proxy-exemption half of
  the existing guard matched only literal dial addresses, so the exemption
  never found the proxy — the code comment documented this as a limitation.
- **The allowlist now matches by resolution.** When a dial to a private
  address does not match the allowlist verbatim, the remembered proxy names
  are resolved and their addresses compared against the dial target. The DNS
  answer is cached (`golang.org/x/sync/singleflight` collapses concurrent
  dials into one lookup, 5 min TTL for answers, 30 s for failures), so the
  hot path keeps paying no lookup. The exemption stays the proxy itself, not
  its range: a dial to `198.19.0.1:9000` or to any other host on the same
  address is still refused, and the reserved ranges remain blocked as request
  targets — including Alibaba's metadata endpoint at `100.100.100.200`,
  which is why they were added.
- The shared lookup runs detached from the caller's context
  (`context.WithoutCancel`, the same pattern as `live_catalog.go` and
  `codex_proxy.go`): a singleflight flight inherits its first caller's
  context, so an unrelated client hangup mid-lookup would otherwise cancel
  the lookup for every concurrent waiter and cache the cancellation as a
  failure — refusing every proxied dial for the failure TTL and
  re-introducing this symptom from an aborted request.
- Verified in `internal/proxy`: resolved-match exempts the configured proxy
  while an unrelated port on the same address and a proxy resolving elsewhere
  stay blocked, a real request through a `localhost:3128` proxy reaches it
  end-to-end (`TestSafeDialerMatchesAProxyWrittenAsALiteralAddress`,
  `TestRequestThroughAHostnameProxyReachesIt`), and a cancelled lookup does
  not poison the shared failure cache
  (`TestCancelledLookupDoesNotPoisonTheResolutionCache`).
