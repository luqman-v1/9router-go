### Kiro: Builder ID / social connections without profileArn get the public default

- Kiro answered `400 profileArn is required for this request.` for OAuth connections (Builder ID, Google/GitHub) that had no `profileArn` in `providerSpecificData`. The executor sent the request without the field.
- OAuth/social connections without a stored ARN now get the public default profile for their sign-in method: Builder ID for `builder-id`, the social profile for `google`/`github`. An explicitly stored `profileArn` still wins.
- Account-bound auth (`idc`, `external_idp`, `api_key`) still sends no default: that ARN belongs to another account there and returns 403.
- The selection lives in `internal/providers/kiro_profile.go` (`KiroEffectiveProfileARN`), ported from upstream `decolua/9router` v0.5.109 (`open-sse/config/kiroConstants.js`, `open-sse/translator/request/openai-to-kiro.js`).
- Verified with a table test over auth method and explicit ARN (`kiro_profile_test.go`), envelope tests (`kiro_profile_arn_test.go`), and `go vet` / `go test` for `providers`, `translator`, `proxy/executor`.
- The usage path (`usage_providers.go`) is unchanged: it already falls back to the default for `idc`/`external_idp`, as upstream `services/usage/kiro.js` does.
