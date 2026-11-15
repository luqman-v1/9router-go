### Gemini 4 Hydrogen model, capabilities, xhigh thinking level, and pricing

Adds `gemini-4-hydrogen` plus level variants (`-high`, `-medium`, `-low`, `-xhigh`)
to the registry for `ag`, `antigravity`, `gc`, `gemini`, `gemini-cli`, `gemini-web`,
`gh`, `github`, and `vertex`. Mirrors Gemini 4 Argon wiring (gemini-level thinking,
1M context window, $4/$20 intro pricing) but adds an `xhigh` thinking tier above
`high`.

- `ProviderModels` registry lists hydrogen variants on all upstream-facing
  providers (`ag`, `antigravity`, `gc`, `gemini`, `gemini-cli`, `gemini-web`, `gh`,
  `github`, `vertex`).
- `patternCapabilities` gains `*gemini*hydrogen*` row: vision/PDF/audio/video,
  reasoning/search/tools, `gemini-level` thinking format, thinking cannot be
  disabled.
- `patternThinking` gains `*gemini*hydrogen*` row advertising
  `low|medium|high|xhigh` (ordered before `*gemini-4*` so Hydrogen claims its own
  row); `xhigh` clamps to itself, `minimal`/`none` clamp to `low`.
- Pricing: `gemini-4-hydrogen` and level variants at $4 input / $20 output per 1M,
  cached 0.2, reasoning 20, cache creation 0.375 (mirroring Argon's 2x over
  baseline).
- `internal/pricing/tables.go` pricing rows for `gemini-4-hydrogen-xhigh` (the
  new tier not present on Argon).
- Dashboard `web/src/lib/models.ts` blocks updated for `ag`, `gc`, `gemini`,
  `gemini-web`, `gh`, `vertex` so routed models appear in the dashboard catalog.
- Regression test `internal/providers/gemini4_hydrogen_test.go` covers caps,
  thinking levels, clamp, pricing, and provider registration.

Tests: `go test ./internal/providers/ ./internal/pricing/...` pass.
