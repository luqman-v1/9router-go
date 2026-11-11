### fix(free-pi): stop relaying the ad gate as if it were the model's answer

free-pi's ad gate does not always arrive as an error. Measured live, a gated
account received:

```
200 {"id":"chatcmpl-freepi-…",
     "choices":[{"message":{"content":"free-pi is free because it shows ads,
       and this session hasn't loaded any. Requests stay paused until ads load…
       Your free-pi-cli is out of date: update with `npx free-pi-cli@latest`."}}],
     "usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}
```

We relayed that verbatim, so an API client read upstream's upgrade notice as
the model's reply. Nothing caught it: the existing gate handling keys on a 403
carrying `Ads aren't showing for this account` (`providers/freepi.go`), and the
generic 200-body guard (`proxy.EmptyUpstreamError`) accepts any body with
non-empty content — verified, not assumed.

Two signals now identify the gate together: one of its own phrases, and a
completion that consumed **zero** tokens. The wording alone could in principle
appear in a real answer discussing free-pi's business model; zero completion
tokens alongside non-empty content cannot — a model that emitted text spent
tokens producing it. Requiring both keeps genuine answers safe, and the marker
list is deliberately narrow (a bare `"ads"` would flag any answer about
advertising).

A detected gate becomes a real `403` naming `free_pi_ad_gate`, with a message
that tells the client what actually clears it. It is not retried: no retry can
fix a gate that is genuinely closed, and `403` is already in
`providers.RetryableStatusCodes` so a combo advances to the next member.

**This does not make gated requests succeed, and it must not.** The gate is
upstream's real policy, and this change only stops us from misreporting it as
an answer. What clears it is a genuine ad impression: open the free-pi
connection page in the dashboard, which renders a creative this gateway served
and reports the impression once the client confirms it painted. Fabricating
impressions — as the reference proxy does — is still deliberately not
implemented; a false impression is a false claim to the advertiser whose spend
funds the inference.

Covered by 10 new cases in `free_pi_gate_test.go`, including the measured body,
an SSE `delta`-shaped gate, and two negative controls proving a real answer
that mentions ads is preserved (and one proving gate wording with non-zero
token spend is preserved too), plus an end-to-end pair through `ForwardFreePi`.
