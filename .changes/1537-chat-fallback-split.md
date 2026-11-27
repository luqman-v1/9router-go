### Changed

- Split `internal/handlers/chat/fallback.go` into same-package files by responsibility: account fallback orchestration, forward path, error/reset parsing, opencode refusal classification, token-saver context helpers, and protocol/body-shape detection. Behavior unchanged; all existing `internal/handlers/chat` tests are byte-identical and still pass.

Refs #1516
