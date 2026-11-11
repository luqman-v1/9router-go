### perf(chat): eliminate connection table scan on hot resolution path (#1047 follow-up)

- **Connection Routing Fast Path**: Moved connection ID and name resolution behind provider alias and `KnownProviders` checks so standard `provider/model` requests bypass the DB entirely, matching baseline performance (25 allocs/op, 888 B/op).
- **Targeted Connection Lookup**: Replaced `GetProviderConnections("", false)` with lightweight `Repo.ResolveConnectionRoutingByPrefix` querying only `provider, id` without credential hydration or JSON parsing.
- **Provider Alias Shadowing Guard**: Ensured connection names cannot shadow real provider aliases (e.g. connection named `deepseek`).
- **Scoped Mistral Badges**: Limited `VIBE CODE` and `API PAYG` badge display in `ProviderDetailView` strictly to Mistral.
- **Catalog Cleanup**: Pruned retired Mistral models (`zai-glm-5-2`, `devstral-2512`, `labs-devstral-small-2512`).
