### Benchmark the real gateway instead of an in-process fake proxy

`benchmark/runner.go` used to start its own `startProxyServer` — a small
hand-written HTTP forwarder — and then measured *that*. The forwarder never
touched a handler, a translator, an executor, auth, or the database, so every
RPS and percentile it printed was a measurement of the Go standard library.
The numbers were not wrong-looking; they were about the wrong system.

The runner no longer boots anything. It takes an explicit target URL
(`-target` / `BENCH_TARGET`) pointing at a gateway the operator already started
on synthetic state, and it neither opens a listener, spawns or kills a process,
nor creates or reads SQLite.

Three things are now measured instead of assumed:

- **Body is read to completion.** `measureOnce` records header latency, first
  body byte, and total time over a fully-read body, so a request that stalls
  mid-stream cannot be reported as a fast one.
- **A terminal event is required.** A response counts as a success only if it
  carries `data: [DONE]`, `"type":"message_stop"`, or
  `"type":"response.completed"` (or is a complete JSON document when not
  streaming). A proxy that dies between headers and body used to count as
  success; now it is a semantic error, and RPS counts successes only.
- **Every request is dispatched.** The work queue is indexed rather than
  `requests / concurrency`, which silently dropped the remainder — 503
  requests over 10 workers fired 500 and the table still looked complete.

The mock upstream now reads `{"stream":true}` from the request body instead of
guessing from the query string and `Accept` header, so a gateway that streams
because the body says so is no longer measured as a JSON response.

`FirstBodyByte` is deliberately *not* labelled TTFT: the first byte of a valid
SSE stream is usually metadata or a keepalive, not the first semantic token.
Real TTFT needs a parser seam per client format, which does not exist yet.
See `docs/parity/benchmark-method.md` for the full method and for what is
still unmeasured.
