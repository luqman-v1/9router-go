### docs(evidence): record the narrow provider page after the capacity-limits change

#432 replaced the inline limits card on the provider page with a one-line summary
and moved the six fields into the node's dialog. The review that followed asked
for the narrow after-state capture; the pair attached to that PR held the wide
still and the narrow *before* recording, so the narrow page after the change —
the one place the summary line has to survive a 562 px screen — was missing.

`evidence/473-narrow-after-capture.md` records it from a real binary: the summary
row at 562 px wraps to two line boxes (448 px, then 58 px) with
`scrollWidth == clientWidth`, the wrap disappears at ~619 px of viewport, and the
fields grid reports `grid-template-columns: 392px` at 562 px against
`188px 188px` at 1024 px. No code changes — the render already behaves as
`ProviderNodeLimitsField.svelte:84-86` describes; what was missing was the
evidence, and it now lives in the repo instead of in a merged PR's attachments.
