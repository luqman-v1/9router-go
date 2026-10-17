### fix(opencode-zen): shape the messages lane by destination gate, not by lane

`forwardZenMessages` applied `ConcealFingerprintTools` — the legacy `/zen/v1`
quartet fingerprint — to every request, and only swapped credentials for
free-tier ids. A free id is forwarded from *every* lane to the keyless
`/inference` gate, and that gate wants the genuine CLI 2.x `read`/`shell`
definitions; sending it the quartet is a body it refuses. The bug was latent
because no free-tier id is served on the messages lane today, which also meant
no existing lane test could see it.

The shaper choice is now `zenShapedBody`, keyed on the gate the request is
destined for (`free`), matching the chat and responses lanes. Pinned by
`TestZenShapedBodyFollowsTheDestinationGate`, which exercises both arms directly
rather than through a lane — verified by reintroducing the old unconditional
legacy shaper and watching the test go red (`a free request must carry the
genuine "read" and "shell" fingerprint, got map[bash:true read:true]`).

### test(opencode): stop classifying an SSE-embedded refusal as a success

The live keyless test asserted success on `Status == 200 && strings.Contains(body, "data:")`.
The gate answers some refusals inside an SSE-shaped body that still carries HTTP
200, so that check reported a closed free tier as healthy — during this
investigation twenty consecutive "200 OK" responses were 175-byte
`FreeTierError` frames.

`classifyLive` now reads the payload: a body containing `FreeTierError` is a
refusal whatever the status line says, and a 200 without completion frames is a
truncated or proxied failure rather than an answer. The live tests report that
verdict instead of asserting availability, because a free tier that opens and
closes is a fact about upstream, not a defect here — and a test that goes red
whenever it closes would be switched off rather than fixed.

Probing the live gate while writing this showed that per-model statuses
(`FreeTierError`, model-unavailable, `ModelDeprecated`) are upstream's own state
and rotate on its schedule: the same request for the same id returned 200
earlier and 403 consistently afterwards, and neither body shaping, session-ID
shape, session reuse, session age (0s to 24h) nor the session counter (1, 2, 5)
changed the outcome while the id was refused. Those findings are recorded here
only to justify the classifier, not as a statement about any model's current
availability.

Verified with `go build ./...`, `go test -count=1 ./internal/proxy/...
./internal/providers/ ./internal/pricing/ ./internal/handlers/chat/
./internal/translator/`, and
`go test -tags=integration -count=1 ./internal/integration/` (69s).
