### fix(media): honor SearchURL on direct search and auto-normalize web combo seats
- **Direct Search Forwarding**:
  - In `internal/handlers/media/media.go`, forward single `/v1/search` requests directly to `providerCfg.SearchURL` when configured, preventing incorrect suffix concatenation `<baseURL>/v1/search` which resulted in HTTP 404 from search providers (such as Tavily, Exa, Serper).
- **Web Combo Resolution**:
  - In `internal/handlers/chat/resolution.go` and `models_list.go`, auto-normalize bare provider names (e.g. `["exa", "serper", "tavily"]`) in `webSearch` and `webFetch` combos to `/search` or `/fetch`, preventing `combo has no valid leaf models` error on legacy combo configurations.
