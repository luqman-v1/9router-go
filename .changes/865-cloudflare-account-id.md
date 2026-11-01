### fix(providers): resolve cloudflare-ai `{accountId}` from connection data

Cloudflare Workers AI requests were failing with an upstream 404 on `/accounts//ai/v1/chat/completions` (empty account segment). The Go port baked the account id into the `cloudflare-ai` registry `BaseURL` from `os.Getenv("CLOUDFLARE_ACCOUNT_ID")`, so a connection whose Account ID lives in `providerSpecificData` (exactly as upstream 9router reads it) still produced an empty segment when the env var was unset — the same SQLite database works on upstream and 404s here.

Fixed to match upstream parity (`decolua/9router` `open-sse/executors/default.js:181`): the registry `BaseURL` now carries the `{accountId}` template, and `getProviderConfig` resolves it from the connection's `providerSpecificData.accountId`, falling back to `CLOUDFLARE_ACCOUNT_ID`, with a clear `requires accountId in providerSpecificData` error when absent. The dashboard validate probe reads the same two sources, and the now-dead `cloudflareAccountFromBaseURL` helper is removed.

Verified: `go build ./cmd/9router-go/` OK; `go test ./internal/handlers/chat/ ./internal/handlers/dashboard/ ./internal/providers/ ./internal/proxy/...` all pass, including the new table-driven `TestGetProviderConfigCloudflareAccountID`. The only `go test ./...` failures are the known `web/dist` placeholder-stub SPA/embed tests, identical on the clean base.
