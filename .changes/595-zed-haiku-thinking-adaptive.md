### fix(zed): send thinking.type adaptive + output_config.effort for claude-haiku-5-5

Zed's Anthropic lane rejects the budget-shaped thinking the global
`claude-haiku-5-5` capability emits: `thinking.type: "enabled"` with
`budget_tokens` is not supported on this model (400: "thinking.type.enabled"
is not supported for this model. Use "thinking.type.adaptive" and
"output_config.effort" to control thinking behavior).

The fix adds a `providerCapabilities["zed"]` row pinning
`claude-haiku-5-5` to `claude-adaptive` thinking format, which emits
`thinking: {type: "adaptive"}` + `output_config: {effort: "..."}` — exactly
the shape Zed accepts. The Zed executor is updated to pass `"zed"` (the
lane id) instead of `"anthropic"` (the wire protocol name) when resolving
capabilities, so this override is scoped to the Zed lane only.

Upstream (decolua/9router) pins the same id to adaptive thinking for the
opencode-go lane, which routes through the same Anthropic backend.

Verified: `EnsureClaudeMessagesFor` under `"zed"` now emits
`thinking.type=adaptive` + `output_config.effort=high`, while the same id
under `"anthropic"` keeps `thinking.type=enabled` + `budget_tokens`
(isolation confirmed).
