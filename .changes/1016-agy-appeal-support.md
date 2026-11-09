### fix(antigravity): store verification and appeal links for agy provider connections

- Scope `agy` (Antigravity CLI) directly into `oauthProbeConfigs` and the cloudcode
  probe switch in `connection_probe.go` so TOS ban detection, appeal URL capture
  (`providerSpecificData.verificationUrl`), and `lastError` redaction work
  identically for `agy` connections without modifying `ProviderAliasMap`.
