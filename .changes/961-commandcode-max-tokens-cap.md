### fix(commandcode): advertise and enforce the real 200000 max_tokens cap

`/v1/models` advertised `maxOutput: 384000` for every `cmc/*` model (ported from upstream `capabilities.js`), but the live `/alpha/generate` endpoint validates `params.max_tokens <= 200000` and rejects larger budgets with `400 Too big: expected number to be <=200000` (upstream decolua/9router#5268). Clients that trust the advertised cap (pi agent, etc.) sent 384000 and hard-failed; a client sending 200000 worked. The requested model `cmc/deepseek/deepseek-v4-flash` hit this on 2026-11-05.

- `commandCodeMaxOutput` lowered to 200000 in `internal/providers/capabilities_commandcode.go`, so `/v1/models` and the dashboard caps advertise the enforced cap. Context window stays 1M (prompt-side validation is separate and not capped at 200000).
- `commandcodeMaxTokens` now clamps a resolved client budget above 200000 down to 200000 instead of forwarding it verbatim (`internal/proxy/executor/commandcode_request.go`), so a stale client still cached at 384000 succeeds instead of 400ing. Budgets at or under the cap, the 1024 default, and null handling are unchanged.

Verified: `TestCommandcodeMaxTokensResolution` adds clamped/exact-cap cases (fail pre-fix); `TestGetCapabilitiesDetailForModel_CommandCode` and `TestHandleGetModelCaps_CommandCode` updated to 200000; `go test ./internal/proxy/executor/ ./internal/providers/` green; dashboard caps test green in isolation (2 pre-existing unrelated failures in the package: allowPrivateNetworking persistence).
