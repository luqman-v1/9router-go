### Fixed

- Quota Tracker: list `agy-acp` connections. The providers-client endpoint filtered through a hardcoded usage allowlist that named only `antigravity`, so ACP rows never reached the dashboard even though the usage endpoint already served their Google quota RPCs.
- Share Antigravity's quota-row grouping with `agy-acp` so session, weekly, and model rows render identically.
- Repair a pre-existing compile error in `models_catalog_cache_test.go` left by the `CreateProviderConnectionFull` context signature change.

Verified with `go test ./internal/handlers/dashboard/` (1518 passed), `bun test src scripts` (1071 passed), and `make vet-svelte` (0 errors). Both regression tests fail on base: the Go test sees an empty provider list, and the frontend test sees ungrouped rows.
