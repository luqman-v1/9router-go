### Changed

- Split `internal/handlers/chat/combo.go` into same-package files along clear boundaries: capability detection/reorder, strategy/weights/sticky state, fallback loop/helpers, and fusion panel/judge handling. No behavior or contract change; existing tests pin the logic.

Refs #1516
