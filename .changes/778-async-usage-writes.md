### perf(chat): move usage persistence off the response goroutine

`logUsage` ran every write it needed — the `usageHistory` row, the
`requestDetails` row, the compression-analytics row, and the daily aggregate
read-modify-write — on the goroutine that had just finished streaming the
response. All of it shares one WAL writer and one global daily-blob mutex, so
completions serialized behind each other, behind the dashboard's own writes,
and behind a daily blob that costs ~21 ms per request once a busy day has
grown it past 400 KB (measured: p50 14.6 ms / p95 92 ms against a 460 KB
blob; p95 11.6 ms contended).

The three persistence units now enqueue onto a single FIFO background worker
that keeps the row → detail → daily order the synchronous path had. The queue
is bounded at 256: overload drops usage rows with a running dropped-total
warning instead of pushing the ledger's backlog onto responses. Metrics, TPM
settlement and the recent-requests ring stay synchronous. Graceful shutdown
drains the queue so the last turns are not billed into nothing. Measured
response-path cost after a busy day: p50 78 ns / p95 97 ns.

Comes with a live timing e2e for antigravity `gemini-3.8-flash-high` (behind
the usual `9ROUTER_LIVE_TESTS=1` gate) and a per-request
`upstream response headers` log on the gemini-native path — the number that
separates "the model was thinking" from "the gateway was slow".
