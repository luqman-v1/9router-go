### Fixed

- **GitHub Copilot Claude 5.5 dotted ids** (`claude-opus-5.5`, `claude-sonnet-5.5`) now carry the same Anthropic per-model thinking rules as the dashed rows (upstream `decolua/9router@f3e1f716`). A thinking-off request from Claude Code's auto-mode permission classifier no longer reaches Copilot's `/v1/messages` shim as `thinking.type: "disabled"` and gets a 400 that held back every Bash call.
  - `Capabilities` gains `ThinkingOffType` and `ForcedToolChoice` (tri-state `*bool`, nil = unspecified) so the Claude request builder can read them.
  - Opus 5.5 (`thinkingCanDisable:false`): a `disabled` thinking block is dropped and `output_config.effort` clamped to `low`; a forced `tool_choice` is downgraded to `auto`.
  - Sonnet 5.5 (`thinkingOffType:"between_tools"`): `disabled` is rewritten to `{type:"between_tools"}` with `xhigh`/`max` effort clamped to `high`; a forced `tool_choice` is downgraded to `auto`.
  - `writeClaudeAdaptive` keeps a requested `display` for always-on adaptive Claude models when the level is not minimal; non-Claude always-on models stay effort-only.
  - Thinking-level fixture divergences added for `github`/`gh`/`tokenharbor` × `claude-opus-5.5` (the v0.5.109 fixture predates f3e1f716; opus-5.5 now correctly drops `none`).

upstream decolua/9router#1287 parity