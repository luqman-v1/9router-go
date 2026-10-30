### Combos: a failed connection is not retried for the same entry

- **A combo entry no longer resends the same request to the same connection.**
  A failure outside the retryable statuses (400 `model_not_supported`, 422,
  500, a provider-limit refusal) neither locked nor excluded the connection, so
  every one of the ten connection attempts for the entry picked the same
  account. On a single GitHub Copilot account a `gpt-6-astra` 400 made 10
  identical upstream calls (~2.7 s) before the combo moved on. Each entry now
  excludes the connections it already tried; the exclusion is per entry, so a
  connection that rejected one model still serves the next member. Matches
  upstream's account loop, which excludes a failed connection
  (`excludeConnectionId`) instead of resending to it.
- Verified: `TestComboNonRetryableErrorTriesEachConnectionOnce` (400/422/500,
  chat and messages: 10 calls before, 1 after), the integration suite, and a
  live GitHub Copilot run (11 upstream calls → 2, ~2.7 s → ~0.85 s).
