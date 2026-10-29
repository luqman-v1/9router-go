---
"9router @ 2026-10-25": grok-web chat executor, Imagine media and the client-tool bridge
---

grok.com's web client now runs end to end: chat completions post to conversations/new through a Chrome-fingerprint TLS transport with the full grok.com Cookie header, mapping the NDJSON reply onto OpenAI streaming or a single completion, with Cloudflare challenges, expired SSO cookies and rate limits each surfacing as their own failure. Declared OpenAI tools are bridged both ways — a CLIENT_TOOLS manifest lets Grok call caller-runtime tools via `<tool_call>` markup, and Grok's native bash/readFile/webSearch/browsePage cards map onto the best-matching declared tool. Grok Imagine joins as `grok-image` and `grok-video` (video never loops — quota is ~1/day), and `grok-4.20`/`grok-4.20-beta` join the catalog.
