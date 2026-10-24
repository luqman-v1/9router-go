### fix(headroom): compress tool outputs on native Responses bodies

Native Responses requests keep tool results under `input[]`, and
`Compress` only understood a top-level `messages` array, so the lane
reached the proxy with nothing to compress.

The eligible plain-text leaves of `function_call_output` items are now
projected into temporary OpenAI tool messages, run through
`/v1/compress`, validated (count, roles, tool-call ids, string
contents), and shorter non-empty text is written back into the original
fields. JSON payloads, reasoning items and instructions keep their
native format, and the committed body can only shrink. The shared
compress round trip is factored into `callCompress` so both paths use
the same guards.

Mirrors upstream decolua/9router a440ef43, Responses lane only: no Go
client lane delivers the Gemini/Vertex/Antigravity envelopes to the
compressor.
