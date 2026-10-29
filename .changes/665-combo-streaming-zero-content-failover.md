### Fixed
- **Combo Fallback**: Fall through to the next candidate model on empty-content streaming responses (`internal/proxy/stream_peeker.go`, `internal/handlers/chat/combo.go`) when an upstream model returns HTTP 200 SSE but terminates without producing content (closes #664, upstream `decolua/9router` commit `ca7b5401` parity).
