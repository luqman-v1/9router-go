### benchmark: measure a real gateway, and measure TTFT at the first semantic event

The runner booted `startProxyServer`, an HTTP wrapper it wrote itself, so the
numbers described that wrapper rather than the gateway. It also required an
explicit sandbox target now: it starts nothing, kills nothing and never writes to
a SQLite file, so a run cannot disturb a live instance.

`FirstBodyByte` was reported as TTFT and never was — a stream that opens with a
keep-alive, a role-only delta or a usage trailer has produced no token at that
byte. The measured point is now the first event that actually carries an answer,
classified per client format from the real translators:

- metadata and keep-alive frames are skipped, not counted;
- tool-only and reasoning-only turns count at the first such delta;
- a typed `event: error` frame or an in-band error object ends the wait as a
  failure rather than an answer;
- a terminal frame or a usage trailer alone does not count as a first token.

The mock upstream now reads `stream` from the JSON body rather than only from
the query string or `Accept`, so a `{"stream":true}` request is measured as the
stream it is, and a malformed trailing document is a 400 instead of a silent
JSON measurement. A `-payload` flag sends a caller-supplied body verbatim, and
the mock answers in the shape the body asks for (tools, reasoning), so a
workload can exercise a lane without a bespoke mock.
