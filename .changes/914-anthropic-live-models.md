### feat(anthropic): list live model catalog in /v1/models

- Register dynamic `ModelsFetcher` for `anthropic` pointing to `https://api.anthropic.com/v1/models?limit=1000`.
- Add `fetchAnthropicLiveCatalog` in `internal/handlers/chat/live_catalog.go` propagating `x-api-key` and `anthropic-version`.
- Gracefully fall back to static model registry on error or absent connection.
- Upstream parity with `decolua/9router#5432`.
