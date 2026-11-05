### Token saver bypass via `X-9Router-Token-Saver`

A request carrying `X-9Router-Token-Saver: off|0|false|bypass` (case-insensitive)
opts out of every token saver — RTK compression, Caveman, Ponytail, ADHD,
Headroom, and system prompt prefix/suffix — and the body is forwarded untouched
with 0 tokens saved.

The bypass is honored on all three chat entry points: `/v1/chat/completions`,
`/v1/messages`, and `/v1/responses` (including `/v1/responses/compact`), each
setting the flag on the request context before any saver runs.
