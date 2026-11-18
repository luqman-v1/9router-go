### opendesign provider (open-design.ai / amr-link)

Add the `opendesign` provider (alias `od`) routing model traffic to
`https://amr-link.open-design.ai/v1` with dual-key auth:

- **Registry**: `ProviderConfig` + `ProviderMeta` (Bearer auth on `Authorization`,
  `Usage: true`, `PassthroughModels: true`, `ModelsFetcher` on
  `https://amr-link.open-design.ai/v1/models`), 12 seed models in
  `ProviderModels["opendesign"]`/`["od"]`, and model formats
  (`claude-haiku-5.5` + `deepseek-v4-pro` → Responses, `glm-5.3-flash` → Claude,
  rest chat).
- **Executor**: `ForwardOpenDesign` — lane-based path selection
  (`/chat/completions`, `/responses`, `/messages`), rejects `vela_ctrl_` keys on
  the model lane, returns `*proxy.UpstreamError` so 401/403/429 are preserved
  instead of being swallowed as transport errors.
- **Usage / Design Plan**: `fetchOpenDesignUsage` reads the account's control
  key (`vela_ctrl_…`) against `https://amr-api.open-design.ai` (separate control
  host, never derived from BaseURL): `GET /api/v1/workspaces/current` (workspace
  id cached per control key ~10 min under `sync.Mutex`, capped at 500 entries
  with expired-then-oldest eviction), then
  `GET /api/v1/workspaces/{id}/billing/preflight`. Credits are parsed with
  `math/big.Rat` so large decimals are not lost to float64; percent =
  `round(max(0, used/limit*100)*10)/10`.
- **Dashboard**: control-key input field in `AddConnectionModal`
  (`buildProviderSpecificData` writes `controlKey` into `providerSpecificData`);
  `opendesign` added to `usageSupportedProviders` and `usageApikeyProviders`.

Tests: table-driven `testing.T` coverage for quota decimal parsing,
window-name boundaries, key classification, workspace caching TTL/isolation,
lane selection, and `od` alias resolution. No `CHANGELOG.md` edit — this
fragment is the entry.

Sources: the `@magpie-community/opencode-opendesign-auth` plugin contract and
the `AstroQore/opendesign2api` API shape (endpoint paths, auth headers, payload
field names). This is a net-new local provider: no upstream `decolua/9router`
parity commit exists for it, so the design was written here from the vendor
protocol — control-plane (`amr-api`) and model-plane (`amr-link`) hosts kept
separate, the `vela_ctrl_` control key confined to the control plane, and
`big.Rat` credit arithmetic so large decimals survive intact.
