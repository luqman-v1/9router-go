### Changed

- Split `internal/proxy/executor/providers.go` into same-package files by provider family/lane: codex, iflow/kimchi, kiro, azure, commandcode, opencode, claude-messages conversion, and opencode-go. No behavior, provider routing, or contract change; all 152 executor test files are byte-identical and pass.

Refs #1516
