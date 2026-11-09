### fix(antigravity): store verification and appeal links for agy provider connections

- Resolve the `agy` Antigravity CLI provider as an alias for `antigravity` so
  TOS ban detection, appeal URL capture
  (`providerSpecificData.verificationUrl`), and `lastError` redaction work
  identically for `agy` connections.
