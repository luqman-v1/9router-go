### fix(compatible-node): align chat path and probe route completion for OpenAI-compatible nodes

- Export `ChatCompletionsURL` with last-segment routing logic so vendor-prefixed bases (e.g. `https://anyrouter.dev/api/v1`) resolve to the chat-completions lane instead of being treated as a named endpoint.
- Add `CompatibleNodeModelsURL` that derives the `/models` endpoint from the completed chat route, so the live catalog fetch, dashboard "Import from /models", the connection probe, and both validate handlers all agree on the same URL.
- `fetchCompatibleNodeModels` falls back to the provider node row's `baseUrl` when a connection carries only the key, so configured nodes publish a live model list.
- Dashboard node/key probes now complete the base URL the same way the chat path does, so a bare host gains `/v1` and a versioned base keeps its prefix.
- `listURL` in the dashboard's connection models handler no longer appends a raw `/models`, avoiding the doubled-version bug (e.g. `/api/v1/v1/models`).
- `lint`: no ineffassign in `connections.go` listURL handling.

Verified: `go test ./internal/handlers/chat/...` and `go test ./internal/handlers/dashboard/...` pass, including new tests covering bare-host, vendor-segment-before-version, versioned, and named-route cases.
