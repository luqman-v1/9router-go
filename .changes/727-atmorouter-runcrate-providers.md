### Features
- **AtmoRouter & Runcrate Providers**: Added two new built-in API-key providers (upstream `decolua/9router#5208` & `decolua/9router#5207` parity, resolving #726):
  - **AtmoRouter** (`atmorouter`, alias `atmo`): OpenAI-compatible gateway (`https://atmorouter.dev/v1/chat/completions`) for Claude, GPT, Gemini, GLM, Kimi, Qwen, and DeepSeek family-prefixed models.
  - **Runcrate** (`runcrate`, alias `rc`): Open-weights provider (`https://api.runcrate.ai/v1/chat/completions`) for DeepSeek, GLM, Qwen, and gpt-oss models, with a dedicated billing balance key validation probe (`https://www.runcrate.ai/api/v1/billing/balance`).
