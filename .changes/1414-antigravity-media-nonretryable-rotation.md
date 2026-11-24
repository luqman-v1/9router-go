### fix(media): stop rotating antigravity image and STT on non-retryable errors and cancel

- **Non-retryable stop & context cancellation** (`internal/handlers/media/antigravity_image.go`, `internal/handlers/media/antigravity_stt.go`): stopped account rotation when `providers.ClassifyError` reports `ShouldFallback == false` (e.g. `400 Bad Request`) or when `r.Context().Err() != nil`, and capped upstream response reads with `io.LimitReader` (`32 MiB` image / `16 MiB` STT).
- **Tests** (`internal/handlers/media/antigravity_image_test.go`, `internal/handlers/media/antigravity_stt_test.go`): added regression tests covering `400` immediate stop, `429` failover rotation, and canceled request context early exit.
