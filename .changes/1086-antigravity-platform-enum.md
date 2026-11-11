feat(antigravity): dynamically compute client platform enum for loadCodeAssist onboarding

Replace the hardcoded `platform: 5` (WINDOWS_AMD64) in lcaMetadata with `GetAntigravityPlatformEnum(runtime.GOOS, runtime.GOARCH)`, mapping darwin/amd64=1, darwin/arm64=2, linux/amd64=3, linux/arm64=4, windows/amd64=5, otherwise=0. Prevents fingerprint contradictions that trigger Google's anti-abuse scanner on non-Windows hosts. Adds table-driven unit tests.

Closes #1083
