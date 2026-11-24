### fix(oauth): guard nil HTTP client and cap response reads in proxy token refreshers

- **Nil HTTP client fallback & bounded reads** (`internal/proxy/oauth/standard.go`, `claude.go`, `xai.go`): added `refreshHTTPClient` with a `15s` fallback `defaultRefreshHTTPClient` so `doFormRefresh`, `refreshClaude`, and `refreshXAI` do not panic when `Params.Client` is `nil`, and capped response reads to `1 MiB` (`maxRefreshBodyBytes`) via `io.LimitReader`.
- **Tests** (`internal/proxy/oauth/standard_test.go`): added `TestStandardRefresher_NilClientFallsBackToBoundedDefault` and `TestStandardRefresher_CapsOversizedErrorBody`.
