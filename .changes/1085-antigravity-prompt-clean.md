### fix(antigravity): strip Claude billing header and sanitize prompt rewrites (#1085)

Strip the Claude Code `x-anthropic-billing-header: ...` line and the competing-client
`"You are a Claude agent, built on Anthropic's Claude Agent SDK."` boilerplate from
system instructions before forwarding to the Antigravity / Google Cloud Code backend,
preventing false 429 `RESOURCE_EXHAUSTED` rejections (#1084).

Port of `ANTIGRAVITY_PROMPT_REWRITES` (decolua/9router `open-sse/config/appConstants.js`).

- `internal/translator/antigravity.go`: `x-anthropic-billing-header:` lines are stripped
  from system instructions via `(?im)^x-anthropic-billing-header:[^\n]*(?:\r?\n)*`, and the
  Claude Agent SDK sentence is removed by the competitive-prompt blacklist, both applied
  in `StripCompetitivePrompts` ahead of `WrapForAntigravity` forwarding. Normal system
  instructions pass through untouched.
- `internal/translator/antigravity_test.go`: table-driven `TestAntigravityPromptRewrites`
  covering billing-header sanitization (incl. CRLF line endings), Claude SDK boilerplate
  removal, combined header + boilerplate, and untouched normal instructions.
