### Added

- **Combo member attempt cap**: bounds how many failed accounts one combo
  member may spend before the combo moves to its next model, instead of the
  fixed ten-connection walk that let a member with an exhausted pool hold the
  client for the whole sweep. Set globally with `comboMemberAttemptCap` (0, the
  default, means off; values are clamped to 1..10) or per provider in
  `providerOverrides[provider].comboMemberAttemptCap`, where an explicit 0
  disables the cap for that provider even when a global cap is set. The cap
  counts failed accounts rather than loop iterations, so a connection skipped as
  model-locked or unavailable costs nothing. The control lives under
  *Default Routing Strategy* in Settings and names 3 as the suggested value when
  an operator turns it on. Both fallback loops are covered by tests that assert
  the upstream call count: `/v1/chat/completions` and `/v1/messages`.

Provenance: operator feature from `9router-pro` `v0.5.109-pro.28`
(`comboMemberAttemptCap` plus `comboMemberAttemptCapOverrides`, default 3,
clamp 1..10, in its `src/sse/services/memberFailover.js`). No counterpart in
`decolua/9router`; Pro-only reference, not upstream parity. Two deliberate
divergences: this defaults to off rather than 3, and the cap is applied to both
Go fallback loops rather than Pro's single chat loop.
