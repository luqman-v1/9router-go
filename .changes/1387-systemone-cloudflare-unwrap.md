### fix(systemone): unwrap Cloudflare result envelope in /v1/systemone

Cloudflare's `/ai/run` API wraps the answer as `{ result: {...}, success: true }`. Unwrap `result` when it carries `answers` and top-level has none, matching upstream open-sse/handlers/systemoneCore.js. (Closes #1384; upstream decolua/9router#35b920c66)
