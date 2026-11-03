### The provider catalog has one source of truth: the Go registry

The dashboard's provider catalog — category, icon, colour, auth type, service
kinds, the free-tier fetcher — lived in two places. `internal/providers` decided
how a request routed, and `web/src/lib/providers.ts` decided how a provider was
rendered, as a 2455-line TypeScript literal covering 149 providers. A provider
added to one and not the other either vanished from the UI or rendered as a bare
id, and nothing caught it: the two files share no test and no import.

`internal/providers/provider_registry.go` is now the single source of truth,
generated from that literal so the port is lossless, and
`GET /api/providers/catalog` serves it grouped by the section the dashboard
renders. The SPA fetches it before mounting and reads the same objects it always
did, so every consumer (`PROVIDER_CATALOG`, `PROVIDER_CATALOG_MAP`,
`getProvidersByKind`, `isChatProvider`) keeps working unchanged.

`ClassifyProvider`, `SectionCategory` and `GetCatalogByCategory` mirror the
upstream section grouping, including the fold of `free` into `Free Tier`.
`GetProviderMeta` resolves a short alias first — `ag` and `antigravity` must
report the same icon and auth type, or the same provider renders two ways
depending on which spelling a connection row stored. `NoAuthProviders` keeps the
`llm` requirement arewedaks documents: `AuthType: "none"` alone also matches the
local media servers, and promoting those onto the providers page is noise an
operator cannot act on.

The registry is emitted in `CategoryOrder` with a per-section count and the
service-kind vocabulary, so the client derives neither.

One trap worth naming: a route added to `dashboard.RegisterRoutes` alone is
reachable in a unit test and 404s on the real server, because that table is
mounted by tests only. The catalog route is registered in
`internal/handlers/router.go` as well — the same pair the per-node limits routes
already document.
