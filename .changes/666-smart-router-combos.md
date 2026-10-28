### feat(combos): Smart Router (System One) strategy with adaptive session stickiness

upstream decolua/9router f3f86fdd parity (#623, upstream #4975). The `smart`
combo strategy asks a System One decision model which member should serve the
request before dispatch, and pins the chosen model per client session so later
turns reuse the provider holding the warm prompt cache. Turn 1 evaluates and
orders by the choice (a decision below the confidence floor keeps the combo's
own order); a pinned session reuses its model — Strict Lock without
re-evaluating, Smart Switch re-evaluating and switching only at or above the
threshold; Per-Turn skips pinning. Every failure path fails open to the combo's
own ordering, and a pin whose model just failed is dropped so the next turn
re-evaluates.

The evaluation resolves the configured router model (default `oc/jev-1.13-free`,
NoAuth), reuses the NoAuth/OAuth credential paths, and posts the choice
question under the same `/systemone` URL rules the media lane uses, bounded at
8s. One deliberate divergence from the literal upstream: Responses
`input_text`/`output_text` parts are read as prompt state — upstream reads
`text` parts only, which left a /v1/responses client's state permanently empty.
Session pins live in a bounded store (5000 entries, 2h TTL, oldest quarter
pruned at the ceiling). `ComboStrategy` settings gain `smartPolicy`,
`sessionSticky`, `confidenceThreshold` and `routerModel`; the dashboard gains
the smart strategy option, the Router picker chip, and the SmartPolicyModal
(Smart Switch / Strict Lock / Per-Turn / Custom Threshold with presets).

Verified: 23 new subtests in `smart_router_test.go` covering prompt-state
extraction, the route-answer shape, ordering, the threshold ladder, the sticky
store, `resolveSmartChoice` against a fake System One upstream (including
fail-open and both no-op paths), every `applySmartRouting` policy, and the
failure-clear hook; `go test ./...` and the integration suite green; the
svelte-check ratchet at baseline and `oxlint` clean; and a click-through on a
live gateway (policy modal round-trip, badge and router chip rendering, no
console errors).
