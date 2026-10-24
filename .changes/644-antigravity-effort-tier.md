### Antigravity: resolve tiered models from client reasoning effort

- An unadorned tiered model id (`gemini-3.8-flash`, `gemini-4-argon`, claude 5.5, ...) now follows the client's reasoning effort to its `-high`/`-medium`/`-low` backend variant instead of always landing on the bare id's tier, mirroring upstream `getModelUpstreamId`.
- The tier comes from the model id's own `(level)` suffix first, then `reasoning_effort` / `output_config.effort` / `reasoning.effort`, then `thinking.budget_tokens` (>16384 high, <=4096 low, else medium); `xhigh`/`max` fold onto `high`, `minimal` onto `low`.
- Unknown tiers and untiered models keep the previous resolution.
