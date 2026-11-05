### feat(minimax-code): default X-Mavis-Agent-Id to "main"

- In `internal/proxy/executor/minimax_code.go`, `minimaxCodeHeaders` now always sets `X-Mavis-Agent-Id: main` in the default outbound headers, so requests to the MiniMax Code / mavis gateway carry the agent id even when the connection config defines none in `StaticHeaders`.
- Unit tests in `internal/proxy/executor/minimax_code_test.go` assert `X-Mavis-Agent-Id` defaults to `main`.
