### feat(images): add OpenAI-compatible /v1/images/edits endpoint

- `POST /v1/images/edits` now accepts both standard `multipart/form-data` and `application/json` payloads, bringing full compatibility with OpenAI SDKs and third-party tooling.
- Parses `image` (file upload or URL/base64 Data URI), optional `mask`, `prompt`, `model`, `size`, `response_format`, `background`, `quality`, and `n`.
- Forwards parsed requests to configured image edit providers (Codex, Cloudflare, or OpenAI-compatible backends) via the media forwarding pipeline.
- Verified with table-driven tests for multipart parsing, JSON parsing, error validation on missing required fields, and provider routing.
