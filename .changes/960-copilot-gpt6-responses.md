### copilot: route GPT-6.x / GPT-5.5+ through the Responses endpoint (#960)

GitHub Copilot's GPT-6.x and GPT-5.5+ models are served by the Responses-native
upstream, so requests for them now hit `/responses` instead of the Chat
Completions bridge:

- `executor.IsCopilotResponsesModel(provider, model)` matches `github`/`ghe-copilot`
  provider with `gpt-6.*`, `gpt-6-*`, or `gpt-5.<minor>=5+` (bare `gpt-6`/`gpt-5`/
  `gpt-5.4` excluded; optional provider prefix and `(level)` suffix stripped).
- `ForwardGithub` routes matching Copilot models to the new `forwardGithubResponses`,
  which converts the body via `buildResponsesBody`, strips `temperature`, rewrites
  the base URL to `/responses`, forwards with `stream: true`, normalizes a 400
  context overflow to `context_length_exceeded`, and delegates to `handleCodexStream`.
- `UpstreamSpeaksResponses` gains a `github`/`ghe-copilot` case so `/v1/responses`
  clients targeting these models are recognized as Responses-native (no Chat bridge).
- The chat handler's thinking-dialect selection adds `openai-responses` for Copilot
  Responses models; `ctx` stays Chat-client format so `/v1/chat/completions` clients
  get Responses→Chat translation while `/v1/responses` clients keep their native wire.

Deliberate upstream divergence: this port uses a static prefix/minor list rather than
upstream's reactive 400 fallback, and explicitly excludes bare `gpt-6` (which upstream
treats as codex-only).
