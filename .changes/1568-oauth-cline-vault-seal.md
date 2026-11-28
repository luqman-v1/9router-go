### Fixed

- **Cline OAuth vault sealing and relogin retention**: `HandleClineExchange` now reads and writes connections via `Repo.GetProviderConnectionByID`, `Repo.ReplaceProviderConnectionPayload`, and `Repo.CreateProviderConnectionFull` with request context so credentials are sealed when `ROUTER_MASTER_KEY` is configured and relogin retains prior sealed refresh tokens.
