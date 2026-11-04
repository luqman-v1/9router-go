### fix(claude): only pin thinking temperature when a sampling param is present

- Only pin `temperature = 1.0` on Claude thinking requests when `temperature` or `top_p` was sent in the request.
- Leaves `temperature` unset when no sampling parameter was provided, preventing HTTP 400 "temperature is deprecated" errors on newer Claude models.
- Verified with unit tests in `internal/proxy/executor/claude_thinking_sampling_test.go` (upstream `diegosouzapw/OmniRoute#18639` parity).
