### feat(benchmark): measure TTFT at the first semantic event, not the first byte

The runner used to stop at first-body-byte latency, which is not time-to-first
token: the first byte of a valid SSE stream is usually metadata, a keep-alive or
an empty `data:` frame. `measureOnce` now scans the body event by event through
`internal/proxy.ScanStream` (on an `io.TeeReader`, so the terminal gate still
sees every byte) and timestamps the first event a client can act on — a text
delta, a tool-call delta or a reasoning delta — across Chat Completions, Claude
Messages and Responses. `classifyEvent` is pinned by a 31-case table covering
metadata, role-only deltas, empty content, terminals, usage trailers, tool-only
turns, reasoning-only turns and typed errors.

TTFT gets its own result table with a `samples` count, and prints `n/a` rather
than `0ms` for a level whose streams carried no semantic event — an average of
nothing is zero, and that zero reads as an instant first token.

Measured over the same 7 streaming cells x 5 concurrency levels x 3 reps against
both gateways on the shared loopback mock: 63 000 requests, 0 failures, 0
semantic errors. Go reaches the first semantic event sooner in all 35 pairs:
min 0.12x, mean 0.29x, max 0.57x of Node's TTFT. That is gateway plus translator
time on loopback, not model latency, and the 5 ms mock delay sits on both sides.
