### Parity: upstream decolua/9router v0.5.116..v0.5.119 (issue #1139)

- fix(claude): retire `claude-fable-5.1` from the GitHub Copilot catalogs and `claude-fable-5-1` from `ocz`/`opencode-zen` (upstream 12e7930e); pin `claude-haiku-5-5` to claude-adaptive on the opencode-go and github lanes and to the commandcode format on commandcode (8aef7480).
- feat(claude): resolve the Claude Code CLI fingerprint from the npm feed (`@anthropic-ai/claude-code/latest`, 6 h TTL, CLAUDE_CODE_VERSION pin, never below the bundled fallback) and rewrite `claude-cli/x.y.z` header values per request — chat lane and the usage tracker (upstream 43bc940e).
- feat(provider): add `sensenova-intl` (token.sensenova.ai) registry entry, transport, image lane, and model catalog (upstream 14b27dc7).
- feat(provider): add `synthetic` (synthetic.new) registry entry, transport, model catalog, key validation through `/v2/quotas` (the public `/models` cannot reject a bad key), and a subscription-quota usage handler (upstream ebe398a9 + 402230ed).
- fix(dashboard): validate new fal-ai keys through `GET /v1/models?limit=1` with `Key <key>` auth instead of a paid generation (upstream 0cf07ccf).
- feat(opencode): write effort `variants` built from `GetThinkingLevels` into `~/.config/opencode/opencode.json`, keeping variants the user already tuned; expose `thinking_levels` on `/v1/models` entries (upstream 7abc190d).
- feat(dashboard): shift-click range selection on the proxy-pool list, filtered to visible rows (upstream 12256f72).

Verified: `go build ./internal/...`, targeted `go test` suites (providers, pricing, translator, clientconfig, media, dashboard, chat), `bun test src` (827 pass), `bun run build`, and the svelte-check ratchet at baseline (74, 0 unresolved identifiers). Two pre-existing failures on clean main (`TestLoadConfigFromDotEnv`, `TestRecordConnectionAccountBanFailure_EscalatesAndClears`) are unrelated.
