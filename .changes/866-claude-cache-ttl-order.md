### fix(claude): clean up stale comment on system cache markers (#866)

- Removed stale comment `// Client markers belong to the pre-normalized prompt.` from system prompt caching in `internal/translator/request.go` (earlier system markers are already stripped by #839).
