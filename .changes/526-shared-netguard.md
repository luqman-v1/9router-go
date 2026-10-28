### feat(netguard): one predicate for the addresses an outbound request must not reach

- **The "is this address internal?" question had more than one answer.**
  `internal/proxy` carried a copy of the predicate that decides whether an
  address is private, loopback, link-local, cloud-metadata or reserved, and
  other layers carried their own. The copy in `internal/proxy` had drifted: it
  checked only `net.IP`'s stdlib predicates, so it did not cover
  `100.64.0.0/10` — and that range contains `100.100.100.200`, Alibaba Cloud's
  instance-metadata service. The guard called it public. (`internal/handlerutil`
  kept its own explicit range table, which does cover it; that copy guards a
  different path and is left as it is.)
- **The predicate now lives in `internal/netguard`, once.** It answers the same
  question for every layer that has to ask it, without an import cycle:
  `internal/proxy` already imports `internal/translator`, so the shared copy
  could live in neither of them. `proxy.IsPrivateIP` keeps its signature and
  now delegates to `netguard.IsPrivateOrReserved` — the change is invisible to
  its callers, and the next range that has to be added is added once.
- **The ranges `net.IP` cannot answer for are listed explicitly.** Go's
  `IsPrivate()` covers RFC1918 only, `IsLoopback()` only `127/8` and `::1`,
  and neither knows about the addresses clouds and carriers actually use for
  internal services. The package adds RFC6598 carrier-grade NAT (which is
  where Alibaba's metadata endpoint lives), RFC6890 protocol assignments
  (which is where Oracle's `192.0.0.192` metadata endpoint lives), the
  RFC2544 benchmark range, the deprecated 6to4 relay anycast, the RFC5737
  documentation ranges, the RFC8215 local-use NAT64 prefix and the
  documentation IPv6 prefix.
- **Nothing that was blocked before is unblocked.** The stdlib predicates are
  still applied first and answer the same addresses they always did; the extra
  ranges only add. A caller that legitimately targets a private address — an
  ollama on loopback, a provider behind a Tailscale overlay — is still served
  by `WithAllowLoopback` and `ALLOW_PRIVATE_NETWORKING`, which short-circuit
  the guard rather than needing it to be lenient.

Verified with `internal/netguard/netguard_test.go`, which pins the blocked
ranges (including `100.100.100.200` and `192.0.0.192`) and carries a control
on both sides of the RFC6598 block, so a guard that refused everything would
fail the test rather than pass it.
