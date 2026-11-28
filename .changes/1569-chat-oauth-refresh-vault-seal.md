### Fixed

- **Chat OAuth refresh vault hydration and sealing**: `oauthRefreshNeeded`, `refreshOAuthTokenIfExpired`, `refreshOAuthLocked`, and `forceRefreshLocked` now read via `Repo.GetProviderConnectionByID` and write via `Repo.UpdateConnectionData` so vault-sealed OAuth connections refresh properly and never leak rotated tokens plaintext at rest.
