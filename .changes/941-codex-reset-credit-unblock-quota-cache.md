### fix(codex): unblock quota cache and clear model locks on reset credit, probe, and connection enable
- **Reset Credits**: In `HandleConsumeCodexResetCredit`, clear the in-memory Codex quota block (`ClearCodexQuotaBlock`) and reset health locks when a reset credit is consumed successfully.
- **Connection Enable & Probing**: Clear in-memory quota blocks when a connection is toggled active in `HandleUpdateConnection` or successfully verified in `HandleTestConnection`.
- **Model Lock Path Escaping**: Quote JSON path segments in `clearSpecificModelLock` and `clearConnectionModelLocks` so models containing dots (e.g. `gpt-6.1-sol`) are correctly matched and unlocked in SQLite.
