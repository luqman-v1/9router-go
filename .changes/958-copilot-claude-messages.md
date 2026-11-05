### feat(copilot): route Claude models through Copilot native /v1/messages

- Route Claude models on GitHub Copilot (`github`, `gh`) directly through `https://api.githubcopilot.com/v1/messages` using Claude Messages format.
- Normalize non-Claude models through the standard OpenAI-compatible path.
- Add unit tests in `internal/proxy/executor/github_claude_messages_test.go`.
- Upstream parity with `decolua/9router v0.5.35`.
