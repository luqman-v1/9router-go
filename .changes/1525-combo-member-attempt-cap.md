### Added

- **Combo member attempt cap**: bounds how many failed accounts one combo
  member may spend before the combo moves to its next model, instead of the
  fixed ten-connection walk that let a member with an exhausted pool hold the
  client for the whole sweep. Set globally with `comboMemberAttemptCap` (0, the
  default, means off; values are clamped to 1..10) or per provider in
  `providerOverrides[provider].comboMemberAttemptCap`, where an explicit 0
  disables the cap for that provider even when a global cap is set. The cap
  counts failed accounts rather than loop iterations, so a connection skipped as
  model-locked or unavailable costs nothing, and the control lives under
  *Default Routing Strategy* in Settings.
