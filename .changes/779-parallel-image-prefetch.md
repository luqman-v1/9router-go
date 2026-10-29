### perf(translator): fetch remote inline images in parallel on a shared transport

The inline image prefetch walked image blocks sequentially on a throwaway
transport: every pass built a fresh `http.Client` + `http.Transport`, and
each remote image downloaded one after another. A turn carrying four
screenshots paid four back-to-back TCP+TLS handshakes — seconds of TTFT for
the Gemini-family targets that inline (`antigravity`, `gemini-cli`, `vertex`,
`ollama`, `kiro`, `commandcode`).

The URLs are now collected in document order, downloaded concurrently
(bounded at 4 workers) in waves sized to the budget still open, and the
rewrites are applied in document order on a transport built once per
`allowLoopback` mode and reused across passes. Sizing each wave to the open
budget is what keeps the sequential walk's contract: a refusal never spends
the budget, so a run of broken URLs must not stop the images behind them from
being fetched. The budget is charged at apply time, per inlined image;
unbudgeted URLs keep their remote form. Every SSRF
rule is untouched: URL validation, the dial-guard against private/reserved
targets, redirect re-validation with the 3-hop cap, the MIME allowlist,
magic-byte labelling, and the byte caps.
