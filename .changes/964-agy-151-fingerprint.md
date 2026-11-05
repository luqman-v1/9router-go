### fix(antigravity): bump agy CLI fingerprint to 1.5.1 (cl=1014269460)

Bumps the standalone agy CLI identity from 1.4.0/cl=1000494485 to 1.5.1/cl=1014269460 in `internal/providers/client_identity.go`. Version and cl captured from a live agy 1.5.1 client (release day 2026-11-05, published in iamxeph/pi-provider-antigravity#108); `cl` comes from runtime config rather than a binary constant, so each future bump needs a fresh live capture (documented in code). `os_type/arch` stay pinned to windows/amd64 so every pooled account presents one identical fingerprint.

Verified: `go build ./...` and `go vet ./internal/providers/` clean; `go test ./internal/providers/` passes; chat package builds.
