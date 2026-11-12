### Fixes

- **Proxy: support SOCKS5/SOCKS5h proxy pool types and sync outbound proxy settings from .env**:
  - `internal/handlers/chat/connections_proxy.go`: allow `socks5`, `socks`, and `socks5h` scheme pools to route through `NewSafeTransportForProxy` instead of mistakenly falling through to the direct connection client.
  - `internal/handlers/dashboard/connection_probe.go`: use `NewSafeTransportForProxy` for dialable proxy probe clients and proxy pre-checks so private-IP proxy endpoints are exempted by the safe dialer's allowlist.
  - `internal/config/config.go`: synchronize `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, `NO_PROXY`, `STRICT_SSL`, and `FORCE_FALLBACK_ON_ALL_UNAVAILABLE` from `.env` to `os.Setenv` when unset in the OS environment, mapping `ALL_PROXY` onto `HTTP_PROXY`/`HTTPS_PROXY` so standard library Go `net/http` respects ambient proxies configured via `.env`.
