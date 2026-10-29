### perf(translator): fetch remote inline images in parallel on a shared transport

The inline image prefetch walked image blocks sequentially on a throwaway
transport: every pass built a fresh `http.Client` + `http.Transport`, and
each remote image downloaded one after another. A turn carrying four
screenshots paid four back-to-back TCP+TLS handshakes — seconds of TTFT for
the Gemini-family targets that inline (`antigravity`, `gemini-cli`, `vertex`,
`ollama`, `kiro`, `commandcode`).

The URLs are now collected in document order, downloaded concurrently
(bounded at 4 workers, 16 targets max), and the rewrites are applied in
document order on a transport built once per `allowLoopback` mode and reused
across passes. The budget is charged at apply time, per inlined image, so a
refusal never spends it; unbudgeted URLs keep their remote form. Every SSRF
rule is untouched: URL validation, the dial-guard against private/reserved
targets, redirect re-validation with the 3-hop cap, the MIME allowlist,
magic-byte labelling, and the byte caps.
