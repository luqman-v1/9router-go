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
  message as the answer. A frame split across WRITES is held too, and that hold
  no longer depends on a content heuristic firing on the first half: a 245-byte
  banner frame split at byte 60 lands inside the JSON header, before `content`
  exists, so `HasSSEContent` sees nothing there. Holding only when the first
  write looked like content therefore relayed that banner verbatim — the exact
  failure this layer exists to stop. Any write that ends mid-frame is now held
  (bounded at 64 KiB) and the joined bytes are classified when the frame
  completes; `TestPeekerHoldsSplitBeforeContentField` pins that split position
  and `TestPeekerRelaysSplitRealAnswer` pins that a real answer split the same
  way is relayed byte for byte. An upstream that stops mid-frame fails open at
  `Finish` and relays the bytes.
- Measured on the healthy path with `BenchmarkStreamContentPeekerHealthy`
  (committed; three frames, one write per frame, a counting writer so the
  recorder's own ~1.5 µs does not swamp the result), `-benchtime=2s -count=4`:
  base 695-805 ns, head 899-947 ns, 384 → 400 B, 6 → 6 allocs. The honest
  reading is **+0.15-0.2 µs per streamed turn and no new allocation** for
  classifying the first frame — not the "cheaper than the base" an earlier
  draft of this fragment claimed from benchmarks that carried the recorder's
  setup cost. The supporting costs, measured the same way: `FrameParses` 200 ns
  / 0 allocs per frame (line scan, no `bytes.Split`), the prefilter 126 ns / 1
  alloc, and `IsBannerSuspect` 5 ns on an answer because it rejects on the first
  letter before normalizing. `ExtractSSEText` (the full JSON decode, only
  reached by a frame whose prefilter hit) is 1.7 µs / 6 allocs.
