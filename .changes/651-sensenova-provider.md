### SenseNova provider support

- New API-key provider: chat at `https://token.sensenova.cn/v1/chat/completions`, image generations at `/v1/images/generations`, catalog of 7 models (SenseNova 6.8 Flash Lite, the U1.5 image pair, DeepSeek V4/V4.1 Flash, GLM-5.2, Kimi K3).
- `sensenova-6.8-flash-lite` capability row: vision + reasoning on the openai thinking format, 1M/131k.
- Image options (`output_format`, `response_format`, `watermark`, `prompt_extend`) ride the generic image forwarder unchanged — the Go lane passes the client body through, so no adapter whitelist is needed.
- Connection test rides the generic `/v1/models` probe derived from the chat base URL, with the chat-call fallback.
- Dashboard icon + picker list mirrored in the web SPA.
