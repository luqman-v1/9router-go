### Fixed

- **cline-free/muse-spark contributor models**: requests with `reasoning_effort:"none"` (or `thinking:{type:disabled}`, `(none)` suffix, providerThinking off) now clamp to `"minimal"` instead of forwarding `"none"` to `api.cline.bot`, which rejects it with "Reasoning is mandatory for this endpoint and cannot be disabled". Fix declares the contributor family reasoning-mandatory (`ThinkingCanDisable: false`) in the Go capability table, matching the existing unified-thinking clamp. Intentional divergence from upstream `capabilities.js`, which omits the flag.
