### fix(updater): retain digest-less manifest info as fallback when GitHub Releases fails

- **Manifest fallback on GitHub Releases failure** (`internal/updater/updater.go`): when `version.json` reports an available update without a `sha256` digest and the GitHub Releases API fallback fails (e.g., 5xx outage or rate limit), `CheckUpdate` now caches and returns the manifest's `UpdateInfo` instead of failing with a synthetic `manifestErr`. Also switched `PerformSelfUpdate` to `http.NewRequestWithContext` and `RestartSelf` to `selfExecutable()`.
- **Tests** (`internal/updater/updater_test.go`): added `TestCheckUpdate_ManifestWithoutDigestServesAsFallbackWhenGitHubFails` covering fallback to digest-less manifest metadata when the GitHub Releases API returns `503`.
