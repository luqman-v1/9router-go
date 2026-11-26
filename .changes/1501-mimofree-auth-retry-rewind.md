### MiMo Free client handling, retry rewind, and read caps

- Routed `getMimoJWT` through `h.Client` (with a 15s bounded default client fallback when `h.Client` is nil) instead of `http.DefaultClient`, preventing nil-client panics and honoring configured handler transports.
- Rewound `req.Body` via `req.GetBody()` before retrying 401/403 auth failures in `MimoFreeChat`, and bounded bootstrap and error response reads with `io.LimitReader`.
- Verified with `TestMimoFreeChat_ClientRetryAndCaps` in `internal/handlers/chat/mimofree_test.go` (`go test -count=1 ./internal/handlers/chat/...`, `go vet ./internal/handlers/chat/...`).