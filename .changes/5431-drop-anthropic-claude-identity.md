### fix(executor): drop Claude Code identity system block on Anthropic API requests

- In `ensureMessagesFor`, when provider is `anthropic`, drop leading system
  blocks matching Claude Code identity, Agent SDK, or billing-header text,
  so they are not forwarded to the raw Anthropic Messages API.
- Adds `anthropic_identity_test.go` covering string and array system shapes
  and the provider-gated behavior.
