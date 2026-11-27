### Fixed

- **Freebuff OAuth vault sealing and body caps**: `HandleFreebuffPoll` now persists connections through `Repo.ReplaceProviderConnectionPayload` and `Repo.CreateProviderConnectionFull` with request context so credentials are sealed when `ROUTER_MASTER_KEY` is set, and `freebuff.go` bounds incoming/upstream reads to 1 MiB.
