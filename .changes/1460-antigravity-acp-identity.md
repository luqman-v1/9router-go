### Fixes
- **agy-acp**: Prepend the official Antigravity 2.33.0 identity to the folded ACP prompt so ACP-backed models receive the same identity as the HTTP Gemini path; skip injection when the caller already supplied it.
- **agy-acp**: Deliver streamed answer text in small flushed deltas instead of one large chunk, since the agy ACP agent emits the whole answer in a single notification.
