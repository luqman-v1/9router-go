### Fixed

- Qoder CN PAT exchange now reaches the CN deployment. `qoderResolveCredential`
  passed the raw region string (`"cn"`) to the provider-keyed endpoint table,
  which silently fell through to the intl gateway and sent CN credentials to
  `openapi.qoder.sh`. It now maps the region back to a provider id
  (`qoderProviderIDForRegion`), so a CN Personal Access Token exchanges at
  `openapi.qoder.com.cn` and resolves userinfo there (AGENTS.md §3.A). This is
  the same region-string bug fixed for the catalog fetch in #1274, applied to
  the PAT-exchange path. Local compatibility fix; no upstream citation.

### Verification

- `TestQoderResolveCredential_CNPATUsesCNEndpoints` fails on base (CN credential
  sent to `openapi.qoder.sh`) and passes with the fix; intl and device-token
  pass-through guards also covered.
- `gofmt` / `go vet` clean on the touched package.
