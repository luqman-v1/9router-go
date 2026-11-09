### Gemini thought summaries streamed by default

Gemini 3.8 Flash and Gemini 4 Argon now stream thought summaries during
thinking by default (`includeThoughts: true` on the gemini-level wire
format), keeping the gateway stream watchdog from terminating idle
thinking connections as stalled. Clients can opt out via
`include_thoughts: false` / `includeThoughts: false` in the OpenAI
request body. Explicit opt-out is respected; when the effective thinking
level is minimal (no thinking on the wire), `includeThoughts` stays
false.
