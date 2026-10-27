### Added
- **Model Catalog**: Expose OpenRouter-format model pricing metadata in `/v1/models` (`internal/pricing/format.go`, `internal/handlers/chat/models_list.go`) with per-token decimal strings (`prompt`, `completion`, `input_cache_read`, `input_cache_write`) and per-1M numeric rates (`input`, `output`) (closes #662, upstream `OmniRoute` commit `2319a84a` / PR #17259 parity).
