### fix(combo): stop failover on in-band error frames while maintaining connection lockout

- In combo streaming, treat verified in-band SSE error frames (`event: error` or non-null `error` payloads) as immediate failure signals: stop the combo chain to avoid masking upstream errors, while recording the connection cooldown/lockout so subsequent client retries route to healthy candidates.
- Fixes false-positive in-band detection by requiring authoritative `DetectInbandSSEError` match rather than substring matching `"error":null`.
