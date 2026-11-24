### Fixed

- fix(kimi): recognize Kimi Code official IDs `k3.1-pro` and `k3.1-pro-highspeed` (without the "kimi-" platform prefix) in `internal/providers/capabilities.go` with 1,048,576 context window, 131,072 max output, vision, video input, and Kimi thinking. Previously requests for bare `k3.1-pro` fell back to the 200k floor. Upstream parity: decolua/9router#7d32657ce (#5605). Closes #1393.
