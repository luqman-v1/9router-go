### fix(chat): record a client abort as 499 in the request log

The response path already answered 499 for a client that hung up, but the
request log recorded the same event as 502, so the row and the response
disagreed about what happened.

The abort verdict is taken from the request context — Go's HTTP server
cancels the request context when the client disconnects — and `LogFailure`
was called with a nil context, so the check could not fire there at all.
`LogFailure` now takes the request context, and both paths run the same
classifier on the same context, so the verdict has one source.

Measured on the pre-fix tree and on this one, over the same seeded
request-detail row, reading the `response.status` it carries:

| | status recorded in the row |
|---|---|
| before | `502` |
| after | `499` |

A request that was never canceled keeps its upstream status: a 429 from the
upstream is still recorded as 429.
