### Token saver bypass via `X-9Router-Token-Saver`

A request carrying `X-9Router-Token-Saver: off|0|false|bypass` (case-insensitive)
opts out of every token saver — RTK compression, Caveman, Ponytail, ADHD,
Headroom, and system prompt prefix/suffix — and the body is forwarded untouched
with 0 tokens saved.
