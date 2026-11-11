### fix(relay): forward client IP in Cloudflare and Deno worker templates

- Map incoming `cf-connecting-ip` to standard `x-forwarded-for` and `x-real-ip`
  headers in the deployed Cloudflare Worker relay template, preventing
  `429 rate_limit_error: "Client IP address is required for free usage"` on
  upstreams that enforce per-IP rate-limits (e.g. OpenCode free tier).
- Apply equivalent `x-forwarded-for` / `x-real-ip` preservation to Deno Deploy relay.
- Added regression tests in `internal/handlers/media/relay_worker_code_test.go`.
