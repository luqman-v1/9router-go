### Added

- **Route response headers** (`x-9router-provider`, `x-9router-model`, `x-9router-combo`, `x-9router-attempts`): every successful upstream dispatch now sets informational headers so clients and debugging tools can see which provider, model, combo name, and attempt count served the request. Enabled by default; disable with `NINEROUTER_ROUTE_HEADERS=0`. Wired into single-provider fallback, combo (Chat Completions + Messages lanes), and fusion dispatch paths.
