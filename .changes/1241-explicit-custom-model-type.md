### fix(dashboard): make custom model type explicit

- Adds explicit model type selection (`llm`, `embedding`, `image`, `stt`, `tts`) in `AddCustomModelModal`, matching merged upstream `decolua/9router@850eb573`.
- Prevents non-chat custom models from defaulting to LLM.