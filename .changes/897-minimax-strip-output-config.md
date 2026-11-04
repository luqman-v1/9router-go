### fix(minimax): strip output_config for Claude-compatible endpoints

- Automatically delete `output_config` from Claude Messages payloads destined for `minimax`, `minimax-cn`, `minimax-code`, and `minimax-code-global`.
- Fixes generic HTTP 400 "invalid params" rejected by MiniMax on Anthropic structured output requests.
- Retains `output_config` for native Anthropic lanes.
- Verified with unit tests in `internal/proxy/executor/minimax_output_config_test.go` (upstream `decolua/9router#820` parity).
