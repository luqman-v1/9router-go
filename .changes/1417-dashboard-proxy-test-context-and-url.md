### fix(dashboard): validate proxy test URL and bind HEAD probe to request context

- **Proxy test URL validation & request context** (`internal/handlers/dashboard/settings.go`): validated that `testUrl` in `HandleProxyTest` uses `http`/`https` with a non-empty host (returning `400 Bad Request` otherwise), switched the `HEAD` probe to `http.NewRequestWithContext(r.Context(), ...)`, and bounded request/response body reads with `io.LimitReader`.
- **Tests** (`internal/handlers/dashboard/settings_test.go`): added `TestHandleProxyTest_ValidatesTestURLAndHonorsContext` covering invalid `testUrl` rejection (`400`) and prompt cancellation on expired request contexts.
