### Antigravity

- Detect Google's permanent Antigravity (Gemini Code Assist / Cloud Code) TOS ban: `internal/providers/antigravity_ban.go` adds `IsAntigravityTOSViolation`, `ExtractAppealURL`, `LooksLikeAccountDeactivated`, `IsGoogleAppealURL`, and `RedactAppealURLs` (pure stdlib helpers, no DB/logging/HTTP). A 403 with `error.details[].reason == "TOS_VIOLATION"`, `cloudcode-pa.googleapis.com` + `PERMISSION_DENIED`, or the ban sentence stops the retry loop.
- Capture the appeal URL: the connection probe writes `providerSpecificData.verificationUrl` and redacts the link from `lastError` so the continuation-token-bearing appeal link is never stored or logged in the clear.
- Classify the ban as permanent: `errorclassify.go` adds `antigravity`-scoped 24h cooldown text rules and adds both ban signals to `permanentFailurePatterns` so a lapsed cooldown never turns a banned account green.
- Verified with `gofmt -l` (clean), `go vet` (exit 0), and scoped `go test` runs for `./internal/providers/` and `./internal/handlers/dashboard/` (both PASS).
