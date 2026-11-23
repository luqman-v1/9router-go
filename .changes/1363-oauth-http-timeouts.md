### fix(oauth): enforce request context and timeouts on token HTTP calls

- **Bounded OAuth token refresh** (`internal/providers/oauth.go`): replaced package-level `http.PostForm` (`http.DefaultClient`, no timeout, unbounded body read) with `RefreshTokenWithContext(ctx, cfg, refreshToken)` using `http.NewRequestWithContext`, a `15s` timeout client (`oauthRefreshClient`), and a `1 MiB` `io.LimitReader` cap; `RefreshToken` delegates to it.
- **Context-bound Kiro social exchange** (`internal/handlers/oauth/oauth.go`): replaced `http.Post` in `HandleOAuthKiroSocialExchange` with `http.NewRequestWithContext(r.Context(), ...)` executed via a `15s` timeout client (`kiroSocialHTTPClient`).
- **Tests** (`internal/providers/oauth_status_test.go`, `internal/handlers/oauth/oauth_test.go`): added regression tests covering client timeout enforcement, request context cancellation, and response body capping.
