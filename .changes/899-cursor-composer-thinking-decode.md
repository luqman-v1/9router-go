### fix(cursor): decode Composer visible output after last think tag

- Decode visible answer text appearing after the last `</think>` tag in thinking streams for Composer models.
- Keeps private thinking separated while emitting visible suffix text into assistant content.
- Verified with unit tests in `internal/proxy/executor/cursor_composer_thinking_test.go` (upstream `decolua/9router#1310` parity).
