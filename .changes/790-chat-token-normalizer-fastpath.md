### perf(chat): fast-path the token normalizer on single-field bodies

`normalizeChatTokenFields` (#766) unmarshaled and re-marshaled every outbound
Chat Completions body through `map[string]jsontext.Value` to guarantee that
`max_tokens` and `max_completion_tokens` never travel together to an upstream
that rejects the pair.

The normalizer only ever changes a body that declares **both** fields. A body
that cannot declare `max_completion_tokens` therefore cannot be changed by the
parse, but was paying the full JSON round trip anyway. On an 89 KB body (a
50-turn agent conversation), that was **230 µs, 99 KB, and 9 allocations** on
every request, even when the client sent only `max_tokens` or no budget at all.

`mayCarryCompletionTokens` now gates the parse with two literal byte scans
(`"max_completion_tokens"` and `\u`):

- Bodies without `max_completion_tokens` (the common path):
  - 1 KB: 8.0 µs / 2.5 KB / 9 allocs → **1.4 µs / 0 B / 0 allocs** (5.7× faster)
  - 9 KB: 26.3 µs / 10.0 KB / 9 allocs → **6.6 µs / 0 B / 0 allocs** (4.0×)
  - 35 KB: 96.5 µs / 41.5 KB / 9 allocs → **26.6 µs / 0 B / 0 allocs** (3.6×)
  - 89 KB: 230.1 µs / 98.9 KB / 9 allocs → **66.8 µs / 0 B / 0 allocs** (3.4×)
  - throughput pinned to the `bytes.Contains` rate: **~1.37 GB/s** across all sizes
- Bodies with both fields (the rare failure case):
  - the scan succeeds and the parse runs as before (4.8 µs / 778 B / 18 allocs)

**Why the escape check.** A JSON member name may be written with `\u` escapes:
`{"\u006dax_completion_tokens":1}` declares the key without matching the literal
byte sequence. A scan for the literal alone would skip the normalizer and put
both fields back on the wire. Every body containing any `\u` escape therefore
falls through to the parse.

**Verification.** `TestNormalizeChatTokenFields` adds:
- `both keys escaped are still the keys` — both `max_tokens` and
  `max_completion_tokens` written with `\u006d`, fails when the escape check is
  removed (mutation tested);
- `an escaped value without token fields is untouched`;
- `a differently cased key is not the key` (`MAX_TOKENS` / `Max_Completion_Tokens`).

Benchmark reproducible via `go test ./internal/handlers/chat/ -bench=BenchmarkNormalizeChatTokenFields`.
`go test ./internal/handlers/chat/` green; `go vet` clean.
