### fix(agy-acp): honour cancellation emitted while the prompt result is delivered

- `streamPromptOnce` could return success when the caller cancelled from inside an event callback during the same select iteration that delivered `session/prompt` (CI run 45047563683: `TestPoolCancelledTurnKeepsWorker` saw `nil` instead of `context.Canceled`). A disconnected client could therefore have a cancelled turn billed as a completed one.
- Every event emission now rechecks the caller context and sends `session/cancel` before returning the cancellation error; the existing pre-select check alone could not catch a cancellation raised inside notification draining or final-result delivery.
- Added `TestStreamPromptCancelledWhileEmittingResult`, which fails on the base code and passes with the fix. Verified with `go vet ./...`, `go test ./...`, and `go test -count=20 ./internal/acp/agy/` locally.
