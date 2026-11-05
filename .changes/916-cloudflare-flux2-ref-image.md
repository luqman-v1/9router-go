### feat(media): forward Cloudflare FLUX.2 reference image as input_image_0

- Detect Cloudflare Workers AI FLUX.2 models (`@cf/black-forest-labs/flux-2-dev`, `flux-2-klein-4b`, `flux-2-klein-9b`).
- Forward reference images as binary `input_image_0` in multipart form-data to `<baseURL>/ai/v1/run/<model>`.
- Support `data:` URI, `http(s)` URLs, and base64 payloads with SSRF protection.
- Wire into both direct and combo media dispatch loops.
- Upstream parity with `decolua/9router#5350`.
