### A 200 that carries only an upstream fault banner is retried

- Antigravity's agent path reports a failure as **ordinary assistant text on
  HTTP 200** ("Agent execution terminated due to error …", auth/quota/policy
  faults, generic 502 banners). Every "does this carry an answer" check saw text
  and committed the turn, so combo fallback never advanced and the client was
  shown the error message as if it were the model's reply.
- A turn whose **whole** text is banner-shaped is now classified as retryable:
  the event-stream peek refuses it before the headers are written (the same 502
  path an empty turn takes), and the buffering writer fails it at `Finish`
  instead of committing. On the last combo candidate the caller still surfaces
  the banner, so the operator sees why.
- **This is not upstream parity.** `decolua/9router` has only
  `isTransientAntigravityError(status, message)`; no heuristic treats 200
  assistant text as a fault. The change turns a 200 into a retryable failure and
  therefore changes failover behaviour, so it is graded as net-new semantics.
- The bar is narrow on purpose: every pattern is anchored at BOTH ends, and the
  text must be short. A real answer that merely OPENS with a fault phrase
  ("Quota exceeded is the phrase you asked about; …") does not classify — the
  earlier `^phrase` anchoring did classify it, turning a legitimate short answer
  into a retry, and the four reviewer probes are now negative tests.
- A banner split across frames is held open while the text is still a prefix of
  a known lead-in ("Agent execution" → …), so the client never sees half a fault
  message as the answer. A frame split across WRITES is held too: the first
  half of a banner frame reads as content to the substring heuristics while its
  JSON is still unparseable, so committing there would relay the banner. An
  upstream that stops mid-frame fails open at `Finish` and relays the bytes.
  A stream that starts with `Error` and turns into a real answer commits on the
  next frame, and tests pin both.
- Measured on the healthy-stream path (`BenchmarkHealthyStreamPeeker`, 3-frame
  stream, one write per frame): 5,170 ns/op, 1,527 B/op, 23 allocs, versus
  8,310 ns/op, 2,491 B/op, 33 allocs on the reviewed base — the peek is cheaper
  than the base because the banner classification commits the peeker once and
  every later write short-circuits on `committed`. `ExtractSSEText` reads a
  typed struct instead of `map[string]any`: 1,793 ns / 6 allocs per frame
  against 3,829 ns / 16 for the map shape. `IsBannerSuspect` on a 1,650-char
  text is 58 µs / 5 allocs and only runs while the text is still
  banner-shaped — a real answer outgrows the shape on the first frame.
