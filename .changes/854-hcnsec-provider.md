### feat(providers): add HCNSec (hc) OpenAI-compatible gateway

- Added HCNSec (`hcnsec`, alias `hc`) OpenAI-compatible gateway (`https://api.hcnsec.cn`) relaying GLM, DeepSeek, Qwen, Step, MiMo, MiniCPM, and SenseNova models (porting upstream `decolua/9router` PR #5343).
- Added `hcnsec` provider configuration with chat completions, messages, embeddings, and speech endpoints.
- Registered 19 models in catalog and mapped `ThinkingFormat: "openai"` to keep relayed vendors on standard reasoning effort.
- Added alias `hc` <-> `hcnsec` and capabilities resolution.
- Added unit tests in `internal/providers/hcnsec_provider_test.go`.
