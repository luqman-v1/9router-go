### feat(usage): display average generation speed (TPS) per model in Usage breakdown table

- **Per-model TPS aggregation**: Added `Repo.GetModelTPSSince` to compute aggregate generation speed (tokens per second) for each model from successful `requestDetails` observations within the active time window (e.g. today).
- **Usage stats API**: `HandleUsageStats` now populates `avgSpeed` and `avgTps` on `ByModel` entries when the model view is requested.
- **Breakdown table UI**: `UsageBreakdownTable` displays a dedicated, sortable **Avg Speed** column (`tok/s`) when viewing models in both Costs and Tokens modes.
