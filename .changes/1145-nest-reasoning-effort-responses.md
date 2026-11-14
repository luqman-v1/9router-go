### fix(thinking): nest reasoning effort for OpenAI-compatible /responses nodes

- Nests `reasoning.effort` and removes top-level `reasoning_effort` for OpenAI-compatible nodes using the Responses wire, preventing upstream HTTP 400 parameter errors.
- Parity with upstream `decolua/9router#5611`.
