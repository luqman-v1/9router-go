### Documentation

- **scope:** two web-fetch comments (`internal/handlers/media/media.go`, `internal/handlers/media/webfetch_ssrf_test.go`) that called the dialer's socket-level guard the thing that actually stops a private-address fetch. That guard is skipped while `ALLOW_PRIVATE_NETWORKING` is on (the default), so the `targetPrivate` pre-flight is the only check on that path; both comments now say so. Comment-only, no behaviour change.
