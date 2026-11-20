### Installers: `VERSION=exp` for one-click experimental builds

- `install.sh`, `install-noavx.sh` and `install.ps1` now accept `VERSION=exp`
  (also `experimental`), resolving the newest GitHub pre-release by publish
  date. `releases/latest` skips pre-releases, so the old one-liner silently
  installed stable; pinning `VERSION=vX.Y.Z-exp.N` still works.
- Reason: experimental builds were documented as "grab the asset manually",
  which is exactly the friction the one-line installer exists to remove.
- No jq dependency on Unix; API rate-limit failures produce a clear error.
