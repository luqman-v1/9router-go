### feat(providers): standardize Mistral models catalog, live models fetcher, and pricing

- **Catalog update**: Added modern Mistral models (`pixtral-large-latest`, `mistral-small-latest`, `codestral-latest`, `ministral-8b-latest`, `ministral-3b-latest`, `mistral-saba-latest`, `pixtral-12b-2409`, `codestral-mamba-latest`, `mistral-moderation-latest`, `mistral-embed`) to `registry_models.go` and synchronized with `web/src/lib/models.ts`. Dropped deprecated `mistral-medium-latest`.
- **Live models sync**: Configured `ModelsFetcher` for `mistral` (`https://api.mistral.ai/v1/models`) in `provider_registry.go` so the dashboard can fetch and sync live models via `catalog-sync`.
- **Pricing rates**: Added canonical pricing entries for Mistral models in `internal/pricing/tables.go` and verified pricing calculation in `cost_test.go`.
