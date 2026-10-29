### Custom Headers now reach CodeBuddy

A per-provider Custom Headers entry was accepted by the dashboard and then
dropped before the request left the gateway, so editing `X-IDE-Type`,
`User-Agent` or the `X-Conversation-*` family for `codebuddy-cn` / `codebuddy-intl`
changed nothing on the wire.

- `applyProviderOverrides` now also records the operator's headers on
  `ProviderConfig.HeaderOverrides`, alongside the existing fold into
  `StaticHeaders`. Every forwarder already reads the folded copy, so nothing
  that builds its own map from that field changes behaviour.
- `proxy.ForwardOpenAI` applies `HeaderOverrides` last, after the registry's
  `StaticHeaders` and its own `User-Agent` / `Accept` defaults. That is the
  order upstream uses (`open-sse/executors/base.js:132`).
- `providers.ApplyHeaderOverrides` matches an existing header name
  case-insensitively, so a lowercase `x-ide-type` override replaces the
  executor's `X-IDE-Type` instead of landing beside it and leaving the winner to
  map iteration order.
- Verified against the live upstream: with the family pinned via Custom Headers,
  `www.codebuddy.ai` echoes the operator's `X-Conversation-Message-ID`
  (`6ba7b812-…`) as the completion id instead of a freshly generated one.

Measured while porting, and worth recording because the reverse is often
assumed: the intl gateway does **not** require a fixed conversation id. Freshly
generated `X-Conversation-ID` / `-Request-ID` / `-Message-ID` values pass channel
verification on every request, and reusing one conversation id across turns also
passes. What the gateway actually gates on is the presence of all three headers
and a first message whose role is `system` — omitting the family returns
`400 code 11128 "request illegal"`, and sending the family without a leading
system message returns `400 code 11128 "first message is not system prompt"`.
A static id is therefore a valid operator choice for prompt-cache stability, not
a ban-avoidance requirement.
