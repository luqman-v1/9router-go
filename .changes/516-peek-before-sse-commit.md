### fix(stream): refuse a turn before the SSE header is committed

A 200 whose stream opens with an error frame, an immediate EOF, keep-alives
only, or a bare `[DONE]` carries no turn — but once `200 text/event-stream` is
committed the failure can only be relayed: combo fallback is over and the
account is billed and cooled down for a reply it never gave.

The stream is now peeked before the header write, so an empty or failed turn
is refused while the decision is still reversible and the 502 travels back to
the account/model loop. The returned reader replays the peeked bytes exactly,
so a healthy stream reaches the client byte for byte.

`ScanStreamEvents` is the scanner's new event-level API: it carries the
`event:` and `id:` labels the chunk-level API dropped, and it lets a caller
stop reading early. The labels are not decoration — a provider that reports a
failure as `event: error` puts the failure in the label, and a payload-only
reader sees a JSON object it has no reason to reject. Returning `false` from
the callback ends the scan without reading further and without reporting an
error, which is what lets the peek read one event instead of buffering the
whole response.

`ScanStream` and `ScanStreamWithDeadline` keep their signatures and their
behaviour: they are the same scanner with the labels dropped again.
