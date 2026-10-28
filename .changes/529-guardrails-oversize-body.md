### Guardrails keep oversized request bodies intact

The inbound guardrail tap read `maxScanBody` (8 MiB) bytes and forwarded exactly
that, so any larger prompt or upload reached the handler with its tail cut off —
a corrupt JSON body for chat, silently damaged audio for media. The global body
middleware allows 10 MiB, and 128 MiB on media routes, so the cap is reachable in
normal use.

A body whose declared length exceeds the cap is now forwarded unscanned, and a
chunked body over the cap has the consumed prefix reattached before forwarding
(`joinReadCloser`). The cap still bounds memory; it no longer changes what the
provider receives.

Verified with `TestInboundOversizedBodyIsForwardedUntouched` — both a declared
`Content-Length` and an unknown one (chunked) — plus the existing guardrail and
chat suites.
