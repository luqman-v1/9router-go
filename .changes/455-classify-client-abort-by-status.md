### fix(chat): classify a client abort by status, not by matching an error string

`isClientCanceled` decided a request had been aborted by the client from the
error's text: `errors.Is(err, context.Canceled)`, then a substring search for
`context canceled` and `client closed`.

Both halves are wrong. The string search matches an *upstream* that canceled
its own request or closed its connection — the gateway then reports it as a
client abort (499) and skips the upstream-error counter, so a real provider
failure is filed as a disconnect the caller caused. And the exact
`context.Canceled` test was the wrong granularity: the abort the forwarder
actually returns is wrapped.

The check now asks what the forwarder already knows: the request context is
done, or the error is an `upstreamError` carrying `StatusClientClosedRequest`
(499), the same status the combo path uses for the same condition. An upstream
failure, whatever its text, is an upstream failure.

Verified with `TestParityUpstreamCancelIsNotClientAbort`, which feeds a context
cancellation, a deadline, and two upstream errors whose text reads like a client
abort, and asserts none is classified as one; then cancels the context and
asserts the abort still is. It fails on the pre-fix code.
