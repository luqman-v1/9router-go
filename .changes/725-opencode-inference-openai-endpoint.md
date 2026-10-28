### Align OpenCode free lane with upstream OpenCode 2 / zen-proxy endpoint and client fingerprint

- Port the endpoint migration from `zen-proxy` / OpenCode v2: free-tier chat and
  responses requests now target `/inference/openai/v1` instead of legacy
  `/inference/v1` (the gate's OpenAI-compatible surface moved there; the
  deciding check is the CLI harness fingerprint in the request body, not the
  endpoint path).
- The messages lane stays on legacy `/inference/v1`: the v2 prefix has no
  `/messages` route (404), so a free id on that lane keeps the legacy prefix.
- Rewrite `opencodeInferenceBase` and `opencodeGoInferenceBase` so legacy `/inference/v1`
  and `/zen/v1` paths map to `/inference/openai/v1`; the zen messages lane maps to
  legacy `/inference/v1`.
- Bump `DefaultOpenCodeUA` to `opencode/latest/2.1.4/cli` and `DefaultOpenCodeV1UA` to
  `opencode/1.19.4`.
- Set default `x-opencode-client` on the free lane to `cli` (matching the User-Agent client identity).
