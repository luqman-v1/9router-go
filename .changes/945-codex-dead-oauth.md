### Fixes

- **oauth**: Quarantine dead Codex/OpenAI OAuth grants instead of retrying them in a loop. A terminal refresh rejection (401 `invalid_grant`, 400 `invalid_grant`/`refresh_token_reused`) now stamps the connection `testStatus: reauth_required`, parks it behind the OAuth cooldown so routing skips it, and releases both once a re-login refreshes the token again.
