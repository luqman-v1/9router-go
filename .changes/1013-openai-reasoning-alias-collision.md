### fix(translator): normalize colliding assistant reasoning aliases

Some clients (or replayed tool histories from other models) replay assistant messages that contain both string `reasoning` and `reasoning_content` (or both names are present). When sent to strict OpenAI-compatible providers, these providers deserialize these names as colliding aliases of one field, resulting in HTTP 400 validation error: "duplicate field" or schema collision.

In `internal/translator/openai_filter_messages.go`, `filterMessages` now drops only the colliding string `reasoning` when an assistant message also carries `reasoning_content`:

- Structured reasoning (map/array like `{"effort": "high"}`) is preserved and NOT stripped — only string `reasoning` that collides with `reasoning_content` is removed.
- Explicitly supplied canonical values (`reasoning_content: ""` or `nil`) are retained.
- If only `reasoning` exists (without `reasoning_content`), it is preserved as-is.
- The message is shallow-copied before mutation so original slice elements are not mutated in-place.

Fixes #1012
