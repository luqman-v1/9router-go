### Memoize the provider-catalog grouping

`GetCatalogByCategory`, `NoAuthProviders` and `ServiceKindLabels` derive entirely from `ProviderRegistry`, a package-level literal written once at init and never mutated at runtime. They recomputed the whole thing per request: a map allocation, 151 appends and a `sort.SliceStable` per category.

Measured on the endpoint that serves them, per request:

| | before | after |
|:---|:---|:---|
| `GetCatalogByCategory` | 181.142 µs · 158.349 B · 50 allocs | 2.140 ns · 0 B · 0 allocs |
| `NoAuthProviders` | 40.122 µs | 2.820 ns · 0 B · 0 allocs |

On the 1-vCPU target (§4.G) that was 181 µs of CPU and 158 KB of garbage spent on data that cannot have changed.

The slices are handed out by reference, so each is `slices.Clip`-ed to `len == cap`: an append by a caller must realocate rather than write into the shared backing array, without paying a per-request copy. `ServiceKindLabels` still returns a copy, because its map is the one thing a caller could reasonably write to.

Verified: `go build ./...` clean; `go test ./internal/providers/ ./internal/handlers/dashboard/` pass. Six new tests pin the data (every registered provider present and sorted, the two synthetic custom endpoints, the llm-only rule on the no-auth list, complete coverage of eligible entries) and the sharing contract (stable across calls, append does not escape, the label map is a copy).
