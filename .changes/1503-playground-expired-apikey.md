### Expired API key and nil repo checks in Playground

- Added a nil `h.Repo` check (503) and RFC3339 `ExpiresAt` validation (`400 expired API key`) when resolving `keyId` in `HandlePlaygroundChat`, closing an expiry bypass relative to `middleware.RequireApiKey`.
- Verified with `TestHandlePlaygroundChat_RejectsExpiredAPIKey` and `TestHandlePlaygroundChat_NilRepoWithKeyIDReturns503` in `internal/handlers/dashboard/playground_test.go` (`go test -count=1 ./internal/handlers/dashboard/...`, `go vet ./internal/handlers/dashboard/...`).