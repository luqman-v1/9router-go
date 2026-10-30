### test(integration): live proof that only one token-limit field reaches the wire

`live_sakoo_test.go` is the only live test for #766's single-token-field guard,
and it needs `SAKOO_LIVE_KEY` — a credential nobody on this tree has, so the
guard's end-to-end proof skips everywhere it runs.

This adds a second live lane against `integrate.api.nvidia.com` through a
custom OpenAI-compatible node, gated on `NVIDIA_LIVE_KEY`. It asserts on the
bytes the gateway sends, not on the upstream's answer, because NVIDIA accepts
both `max_tokens` and `max_completion_tokens` with a 200 — probed on 2026-10-29,
streaming and non-streaming alike. A status check alone therefore cannot tell a
gateway that strips the duplicate from one that forwards both; the recorded
body can.

Three cases, all against the real endpoint: a single request, the same request
through a combo (the lane the original report came from, and the one that
reaches `tryForwardWithConnection` from `combo.go` rather than `fallback.go`),
and the two single-field shapes that must pass through unchanged.

**Verified by mutation.** With the guard in `fallback.go` disabled, both
double-field tests fail on the recorded body
(`{"max_completion_tokens":64,"max_tokens":32,...}`) and pass again when it is
restored — so the tests measure the fix, not the endpoint's tolerance.

The model is pinned to `nvidia/nemotron-3-ultra-550b-a55b` because the other
registry entries on this key answered 410 "reached its end of life" when probed;
a test pinned to them would report an upstream retirement as a regression. The
same reasoning drives the skip rule: an upstream-origin failure (410 retired,
404 not-found-for-account, 503 service overloaded) skips, while the
`cannot both be set` message — the one upstream answer that IS this bug — fails
hard.

No credential is required for the suite to pass: without `NVIDIA_LIVE_KEY`
every test in the file skips, as the Sakoo file already does.
