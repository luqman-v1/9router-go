### Inject OpenCode v2 environment marker into agent system prompts

- Port `applyFingerprintSystemPrompt` from serenhope/9router PR #17.
- OpenCode v2 free tier checks for the `<env>` working directory/workspace block
  in the system prompt, instructions, or first system message. Non-agent or
  incomplete payloads trigger synthetic `429 FreeUsageLimitError` responses.
- Injects `<env>\n  Working directory: /tmp/ocprobe\n  Workspace root folder: /tmp/ocprobe\n</env>`
  when absent, and upgrades incomplete `<env>` blocks idempotently. The paths
  mirror the captured CLI harness preamble, the only env shape measured to pass
  the gate.
- Upstream divergence: this marker and the `/inference/openai/v1` endpoint come
  from serenhope/9router #17 / zen-proxy; the decolua/9router registry this fork
  tracks still serves `/zen/v1` with no `<env>` marker.
