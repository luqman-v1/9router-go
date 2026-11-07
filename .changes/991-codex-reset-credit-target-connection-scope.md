### scope codex reset credit health reset to target connection (fixes #950)

Consuming a Codex reset credit called `ResetProviderHealth("codex", "")`, which
iterated over every codex connection and cleared each one's `modelLock_*` fields
and `backoffLevel`. A reset on connection A mistakenly removed intentional
locks and backoffs on connections B and C.

The health reset is now scoped to the target connection via
`ResetConnectionHealthState(connectionId)`, so consuming a credit on one
codex connection no longer unlocks sibling connections of the same provider.

- `HandleConsumeCodexResetCredit` resets only `target.ConnectionID` instead of
  the whole "codex" provider.
- `TestCodexResetCreditConsumeDoesNotUnlockOtherConnections` seeds two locked
  codex connections and asserts the sibling's model lock and quota block
  survive a consume on the other.
