### fix(opencode): read the first SSE frame before committing 200 OK

The OpenCode free-tier gate has two failure shapes that only matter *before* the
response head is written, and both used to arrive after it:

- it answers a refusal **inside an HTTP 200 SSE frame** rather than with a 4xx;
- it can stall, or drop at EOF, before emitting any frame at all.

Once `200 OK` is on the wire the router's failover machinery can no longer act,
so the first case lost the specific verdict and the second left the client
holding a hung stream.

- **`awaitOpenCodeFirstEvent`** (`internal/proxy/executor/opencode_first_event.go`)
  reads exactly one complete `data:` frame — or fails — before the head is
  committed, then returns a reader that replays those bytes ahead of the live
  stream. It is built on the existing `proxy.NewStallReaderWithFirstEvent`, so
  the first-event deadline and the client-context watcher are the same ones the
  Gemini lane already uses rather than a second implementation: a stalled
  upstream is *closed*, which is what unblocks the pending read.
- **An in-band refusal becomes the status it implies.** A `FreeUsageLimitError`
  frame now surfaces as **429** and a `FreeTierError` frame as **403**. This is
  the substantive fix. Measured against the unfixed code, the same body came
  back as a generic **502** stream abort: the gate's actual verdict was lost, so
  `classifyOpencodeFreeTierRefusal` classified on the wrong code and
  `parkRefusedProxyWithPool` parked the egress with the wrong window. The
  refusal is now reported on the error path, where `forwardOpenCodeRotating` and
  the handler's pool walk already look.
- **A prelude larger than `constants.UpstreamErrLimit` is itself a refusal.** A
  stream that sends a megabyte before its first frame is not the SSE the lane
  expects, and holding it would only delay the failover.
- **A stream that ends before its first complete frame fails** rather than being
  reported as a short success. A stream that ends *mid*-frame with a complete
  frame already buffered still succeeds.
- **Only an event stream is gated.** `isEventStreamResponse` guards the read, so
  a provider that answers a streamed request with one JSON document — a mock
  upstream, or a gateway fronting the real one — keeps going through the
  ordinary body reader. Without that guard the gate rejected valid JSON 200s,
  which the existing `ForwardOpencode` tests caught.
- **Wired on all six free-lane response paths**: both `ForwardOpencode` lanes,
  `forwardOpencodeGoFree`, and the three zen lanes. The zen lanes apply it only
  when `free` — a paid lane answers a real status and its body streams directly.

Cost is one bounded read before the client sees bytes. On a healthy stream the
first `data:` frame is the time-to-first-token the client was waiting for
anyway, so the added latency is nil; the win is that a refused or stalled turn
no longer consumes a proxy attempt or misreports its cause.

Verified: `go test ./internal/proxy/... ./internal/translator/... ./internal/handlers/chat/...`
green (the three `internal/proxy/executor/mcodelease` failures are pre-existing
on `main`), `go build ./...` clean. Tests cover the healthy replay being
byte-exact, both in-band tokens mapping to their status, EOF before and mid
frame, leading keepalive blank lines, the oversized prelude, a stalled upstream
bounded by the deadline, context cancellation releasing the attempt, and two
end-to-end `ForwardOpencode` cases. The in-band test was confirmed to fail
against the unfixed code (502 instead of 429/403).
