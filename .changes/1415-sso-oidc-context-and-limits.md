### fix(sso): propagate request context and cap response reads in OIDC discovery and probes

- **Context-bound OIDC discovery & client-secret probe** (`internal/handlers/sso/sso.go`, `internal/handlers/sso/oidc_flow.go`): threaded `ctx context.Context` into `fetchDiscovery` and `probeClientSecret` via `http.NewRequestWithContext` and the shared `15s` `oidcHTTPClient`, and capped discovery, token exchange, probe, and JSON request body reads to `1 MiB` (`maxSSOBodyBytes`).
- **Tests** (`internal/handlers/sso/sso_test.go`): added `TestOIDC_FetchDiscoveryAndProbeHonorContextCancel` verifying prompt cancellation when the request context expires.
