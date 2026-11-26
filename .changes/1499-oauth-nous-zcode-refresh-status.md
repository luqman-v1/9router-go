### OAuth refresh status handling for Nous and ZCode

- `refreshNous` now uses `refreshHTTPClient` (15s timeout when `Params.Client` is nil) and wraps non-2xx responses in `*providers.OAuthRefreshError` with `truncateBody` (200-byte cap) instead of returning a plain 1024-byte error string.
- `refreshZcodeJWT` now checks non-2xx HTTP status before JSON unmarshaling and surfaces `*providers.OAuthRefreshError` with `truncateBody` so terminal 401 and 400 `invalid_grant` failures are classified consistently with other OAuth refreshers.
- Verified with `TestRefreshNous_SurfacesOAuthRefreshErrorAndTruncatesBody` and `TestRefreshZcode_SurfacesOAuthRefreshError` in `internal/proxy/oauth/nous_test.go` (`go test -count=1 ./internal/proxy/oauth/...`, `go vet ./internal/proxy/oauth/...`).