### fix(executor): arm the completion watchdog only when a completion is pending

`completionWatchdog.arm` started its flush timer whenever nothing had been
emitted yet, without asking whether the bridge actually owed a terminal event.
On a stream that was still producing content the watchdog could therefore fire
`PendingCompletionFlushTimeout` after the stream began, hand a fabricated
completion to the client mid-stream, and then sit on `emitted` so the real
`finish_reason` was dropped.

`arm` now also checks `w.bridge.CompletionPending()` and refuses to schedule the
timer while the stream is still live. The timer keeps its original purpose: once
the terminal event is owed but the usage trailer has not arrived, it flushes
rather than waiting forever.

The bridge is now the only thing bounding this wait. `handleStreamResponse` had
been wrapping the upstream in a second stall reader sized to the same
`PendingCompletionFlushTimeout`, so the bridge's watchdog and that reader
expired together: the reader ended the read while the bridge was still holding
the terminal event, and the client got a stream that stopped at `chunk1` with a
completion built from what it had already seen. That wrapper is gone — the
bridge hands `CompletionPending` to `ScanStreamWithDeadline`, which only arms a
bound once a completion is actually owed, so an upstream pausing mid-turn is no
longer cut off at the deferral bound.

Verified with two tests:

- `TestParityWatchdogWaitsForPending` feeds a content delta, waits past the
  flush timeout, and asserts nothing was completed, then feeds `finish_reason`
  and asserts exactly one completion arrives. It fails on the pre-fix code.
- `TestHandleStreamResponse_ActivePauseExceedsCompletionBound` pauses 100ms
  mid-stream with the bound set to 40ms and asserts both chunks and the
  completion reach the client. It needs both halves: it fails with the stall
  reader still in place, and it also fails with the reader removed but the
  watchdog guard absent — without the guard the arm-time check cannot see that
  nothing is owed yet.
