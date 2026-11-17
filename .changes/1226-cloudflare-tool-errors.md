### Fixed

- Keep the Cloudflare error body in attempt logs: unparsed upstream bodies are no longer replaced by the ~160-rune `ForwardOpenAI upstream: forward to <url>` wrapper before truncation (#1226).
- Strip tool definitions and flatten tool turns before forwarding to Cloudflare models not declared tool-capable, avoiding a 400 on multi-turn tool conversations sent through combos (#1226).
