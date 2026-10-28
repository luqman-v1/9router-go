### fix(opencode): strip foreign reasoning and message keys from chat requests

- Sanitize `messages[]` in OpenCode, DeepSeek, and Kimi chat completion paths so foreign keys (`reasoning`, `reasoning_details`, `thinking`, `thought`, `annotations`, `cache_control`) replayed by clients do not reach upstream validators that reject them with HTTP 400 (`[unsupported_parameter] Unrecognized request argument supplied: messages[2].reasoning`).
- Normalize top-level `reasoning.effort` to `reasoning_effort` and strip `reasoning` object on Chat Completions lanes.
- Wire `isRelayConfig` on `ForwardOpencode` chat completions lane so edge relay pools correctly forward with `x-relay-path`.
- Added unit tests and live E2E test (`TestLiveE2E_Opencode_BeamFree_ToolCall`) in `internal/proxy/executor/opencode_test.go` verifying live tool call emission and subsequent turn completion with replayed reasoning.
