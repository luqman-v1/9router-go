### Cline, ClinePass and CodeBuddy model import

The dashboard's "fetch models" button answered "provider does not support models
listing" for four providers that do serve models, because none of them publishes
a plain OpenAI `/v1/models` route.

- **CodeBuddy** (cn + intl) exposes no listing route at all — probed live, every
  candidate path answers 404. Import now serves the catalogue from the Go
  registry, the same list the chat router resolves against, so the button offers
  exactly what a request can reach and there is no second list to drift.
- **Cline / ClinePass** publish live catalogues on their own host. The existing
  branch read `/api/v1/models` for both, which is the full catalogue and carries
  no `cline-pass/*` rows at all — so a ClinePass connection was offered the free
  namespace, and every one of those ids answers 403 ENTITLEMENT_ERROR. The
  subscription namespace is published only on the recommended feed, under its own
  key, so the two are now resolved separately and kept disjoint: `cline` keeps
  everything except `cline-pass/*`, `clinepass` keeps only `cline-pass/*`. The
  free-tier feed merge stays, and stays additive — a dead feed never takes the
  catalogue down with it.

WorkBuddy is deliberately not handled. It is a separate service this build does
not register: no KnownProviders entry, no ProviderModels entry, no executor. A
branch for it could only ever return an empty list, so it joins when it is
registered.

Non-chat surfaces are rejected by name on segment boundaries only, matching
`providers.IsDiscoverableAntigravityModel`, so an id that merely contains the
letters is never dropped.
