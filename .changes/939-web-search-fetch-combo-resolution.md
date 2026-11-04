### fix(media): honor SearchURL on direct search, prioritize combos, and fix web combo UI
- **Direct Search Forwarding**:
  - In `internal/handlers/media/media.go`, forward single `/v1/search` requests directly to `providerCfg.SearchURL` when configured, preventing incorrect suffix concatenation `<baseURL>/v1/search` which resulted in HTTP 404 from search providers (such as Tavily, Exa, Serper).
- **Web Combo Prioritization & Resolution**:
  - Default `/v1/search` to `search-combo` (and `/v1/web/fetch` to `fetch-combo`) when configured, prioritizing combo failover over single provider fallback.
  - In `internal/handlers/chat/resolution.go` and `models_list.go`, auto-normalize bare provider names (e.g. `["exa", "serper", "tavily"]`) in `webSearch` and `webFetch` combos to `/search` or `/fetch`, preventing `combo has no valid leaf models` error on legacy combo configurations.
- **Web Combo Management UI**:
  - In `web/src/components/media/MediaWebView.svelte`, append `/search` or `/fetch` when clicking providers to add them to combos (e.g. adding `antigravity/search` instead of `antigravity`).
  - Badge active default combo in Web Fetch & Search view.
