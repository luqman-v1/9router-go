### Gemini image adapter with reference inputs + quotaResetTimeStamp

- Gemini image generation answers the native `models/<model>:generateContent` shape instead of the OpenAI images endpoint: reference images (data URIs or base64) become inlineData parts ahead of the prompt, and inlineData parts of the reply convert back to `b64_json`.
- Google's quota-window timestamp (`error.details[].metadata.quotaResetTimeStamp`) pins the model lock on both the Gemini and Antigravity image error paths, far more accurately than the exponential backoff.
- The MITM directory is auto-created on startup in this port already (`internal/mitm/manager.go`), and Antigravity image requests already carried reference inputs, so those upstream pieces needed no code here.
