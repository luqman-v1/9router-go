### feat(providers): give Vertex, Perplexity Web and gemini-cli working executors, and surface the custom system prompt

Four provider lanes were registered in the catalogue but had no executor behind them, so a request to any of them POSTed an OpenAI body at a host that serves a different protocol.

**Executors**
- **Vertex / vertex-partner** (`ForwardVertex`): the registry entries carried no executor, so every request went to `aiplatform.googleapis.com` as an OpenAI body. Both ids share one implementation; the URL shape is decided inside from the provider id (`vertex` for the publisher-model path, `vertex-partner` for the partner-served one).
- **Perplexity Web** (`ForwardPerplexityWeb`): not OpenAI-compatible in either direction — the request is a `query_str` envelope and the answer is Perplexity's own event stream — so the pass-through could not serve it.
- **gemini-cli**: the lane shares a host family with antigravity but the envelope is different in kind. `ForwardGemini` now wraps for Cloud Code Assist (`{project, model, request}`, no `userId`/`requestType`/synthetic `requestId`), posts to `:generateContent` on the `v1internal` root, and sends the CLI's own `User-Agent`, `X-Goog-Api-Client` and `Accept`. `ProviderConfig.IsGeminiNative()` covers `gemini-cli` as well as `gemini-native`, because both speak a Gemini envelope and route through the same native handler.
- `gemini-cli`'s `BaseURL` moves from the `daily-` bypass host to `cloudcode-pa.googleapis.com/v1internal`, which is where the CLI's project discovery actually lives.

**Header parity**
`GeminiCLIApiClient` and the CLI version in `GeminiCLIUserAgent` are pinned to what the current CLI sends (`google-genai-sdk/1.41.0 gl-node/v22.19.0`, `GeminiCLI/0.34.0`), matching the upstream registry. The comment in `gemini_cli.go` already notes that a model-id mismatch in the UA is answered with 403; a stale *version* is the same class of fingerprint mismatch, and it fails silently. A parity test pins both constants so the next upstream bump shows up as a test diff rather than a production 403.

**Custom system prompt**
`TokenSaverView` now exposes the `systemPromptEnabled` / `systemPromptPrefix` / `systemPromptSuffix` settings the engine already reads (`fallback.go:1466`). The prefix leads the system prompt and the suffix lands after it, where it survives a turn of untrusted content.

**Verified**: `go build ./...`, `go test ./internal/proxy/executor/ ./internal/translator/ ./internal/providers/`, and `bunx oxlint --deny-warnings` all pass.
