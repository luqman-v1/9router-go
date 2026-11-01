### Added

- **Bedrock Responses reasoning normalization & Claude adaptive capabilities** (upstream #5311):
  - Add dashed pattern capabilities for Bedrock Claude Opus models (`*claude*opus-4-7*`, `*claude*opus-4-8*`) mapping to `claude-adaptive`.
  - Update `*claude*fable*` and `*claude*mythos*` capabilities to always-on `claude-adaptive` (1M context, 128k maxOutput).
  - Preserve client-provided nested reasoning fields (such as `summary`) when merging top-level `reasoning_effort` in Responses API transformation.
