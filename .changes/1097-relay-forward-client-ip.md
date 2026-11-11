### fix(relay): forward client IP across all edge relay templates

- Map incoming platform-specific client IP headers to standard `x-forwarded-for`
  and `x-real-ip` headers across all four edge relay templates:
  - **Cloudflare**: `cf-connecting-ip` (stripped by Cloudflare on outbound fetch).
  - **Vercel**: `x-vercel-forwarded-for` (platform-verified client IP).
  - **Netlify**: `x-nf-client-connection-ip` / `client-ip`.
  - **Deno**: `x-forwarded-for` / `x-real-ip`.
- Fixes `429 rate_limit_error: "Client IP address is required for free usage"` on
  upstreams that enforce per-IP rate-limits (e.g. OpenCode free tier) when routing
  through proxy pools.
- Added regression tests for all four templates in
  `internal/handlers/media/relay_worker_code_test.go`.
