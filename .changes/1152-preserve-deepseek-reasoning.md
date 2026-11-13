### fix(translator): preserve DeepSeek reasoning_content across message conversions

- Preserves `reasoning_content` on assistant message turns across request translations and Gemini thought mappings, preventing DeepSeek HTTP 400 multi-turn errors.
- Parity with upstream `decolua/9router#1099`.
