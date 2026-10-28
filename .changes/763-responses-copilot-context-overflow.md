### Bug Fixes
- **responses**: Preserve GitHub Copilot context overflow errors as `response.failed` event for streaming `/v1/responses` clients. Normalizes Copilot HTTP 400 `invalid_request_body` with `"Your input exceeds the context window..."` to standard `context_length_exceeded` error code and emits a terminal `response.failed` SSE event (upstream decolua/9router#5280 parity).
