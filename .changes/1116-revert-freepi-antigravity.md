### fix(regression): revert free-pi inband ad gate and antigravity session headers

- Revert PR #1089 (commit `6a49bc16`): Remove `X-Goog-User-Project`, `X-Machine-Session-Id`, `X-Client-Name`, and `X-Client-Version` header injection from `ForwardGemini`. Antigravity consumer/onboarding projects do not have the Cloud AI Companion API enabled for Google Cloud project billing, causing Google Cloud Code Assist WAF to reject calls with HTTP 403 `PERMISSION_DENIED` / `SERVICE_DISABLED`.
- Revert PR #1104 (commit `b5f058fc`): Remove `freePiInbandGateError` from `ForwardFreePi`. Upstream free-pi ad-gate wording was converted into a hard HTTP 403 `free_pi_ad_gate` error, terminating client connections (such as CLI and IDE agents) that do not trigger browser impressions.
- Revert PR #1085 (commit `82eb07c2`): Clean up unit tests associated with prompt rewrites.
