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
- The bar is narrow on purpose: the match must cover the entire text and be
  short. A real answer that merely mentions "capacity", "timeout" or "error"
  mid-sentence never matches, and one test pins that.
- A banner split across frames is held open while the text is still a prefix of
  a known lead-in ("Agent execution" → …), so the client never sees half a fault
  message as the answer. A stream that starts with `Error` and turns into a real
  answer commits on the next frame, and one test pins that too.
