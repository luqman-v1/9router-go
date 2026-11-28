### ✨ FEATURE: Request-level provider routing preferences

- **A client can now steer which providers serve one request.** Pass a
  `provider` object (`order` / `only` / `ignore` / `allow_fallbacks`) in the
  body of `/chat/completions`, `/messages` or `/responses`, the contract
  OpenRouter made standard for multi-provider routers: a combo's member
  list is filtered and reordered by the steer, so `{"ignore":["deepseek"]}`
  removes that member from the candidate set entirely — the excluded
  upstream receives no request, not a failed attempt and a fallback.
- **A single-model request the preferences exclude fails 400 naming the
  steer**, rather than silently routing to the provider the client asked to
  avoid. An empty survivor set is an error everywhere, never a silent
  fall-through to the unfiltered list.
- **`allow_fallbacks: false` turns `order` into a hard allowlist** — with
  it set, nothing outside the order list may serve the request; unset it
  defaults to true and `order` is a preference. Provider ids match the
  canonical spelling either way (`oa` and `openai` are one name), and a
  combo member on a custom provider node matches by that node's id.
- **The object never leaves the gateway.** It is parsed once per request
  and stripped before translation, dispatch and cache hashing, so a strict
  upstream never sees the client-only field. A body without the object is
  not re-parsed at all — the steer costs nothing on the request path, and a
  malformed object is ignored rather than failing a working request.
- **Provenance: OpenRouter's provider preferences object.** This is a
  native feature, not an upstream parity port; `sort`, `zdr`, `max_price`
  and `data_collection` are deliberately out of v1 — the combo strategies
  and the breaker/health stack already own load selection, and those
  fields would duplicate a decision the operator can already configure.

Verification: `go test ./internal/handlers/chat/` — table-driven cover of
the parse, the strip, the candidate filter (only / ignore / order /
hard-order / alias / node-prefix / empty-set error), resolution through
combos and single models, and a handler test that dials a mock upstream
and asserts the forwarded body carries no `provider` field while an
excluded provider answers 400. `go test -tags=integration
./internal/integration/` — four cases through the production router
(`app.ProvideRouter`, real SQLite, fake upstreams): `ignore` steers a
two-provider combo and the excluded upstream receives zero requests,
`only` rejects an excluded single model, `order` + `allow_fallbacks:false`
never attempts the out-of-order member, and the object steers the
`/v1/messages` lane as well. Full suite: `go test ./...` plus the
integration suite pass; the only failures on this host are reproduced
identically on a pristine `origin/main` worktree (documented host-DNS and
live-daemon-port cases). All six `CROSS_TARGETS` build with `CGO_ENABLED=0`
and `govulncheck` reports nothing.

## Review round (changes requested, now fixed)

- **The steer now reaches both cache keys.** `BuildCacheKey` and
  `HashRequest` take the canonical steer bytes (a nil steer adds nothing, so
  every pre-steer key is byte-identical), and the handler puts it on the
  context so the shared forward path stores under the same key it looked up
  with. Resolution still runs after the cache read, so a cached answer for a
  retired model still serves — the steer separates the namespaces instead.
- **The strip is a token-level splice, not a map round-trip.** A
  `map[string]any` decode turned integers past 2^53 into float64
  (`9007199254740993` → `...992`) and re-marshalled non-deterministically,
  breaking the exact-prompt key on precisely the bodies this feature
  touches. The new implementation streams jsontext tokens and copies every
  non-`provider` member verbatim.
- **Single-model requests on a custom provider node match the prefix.**
  `resolvePrefixProvider` resolves to the node's id, so a client steering by
  the prefix it typed (`ignore:["bn"]`) failed open. The single-model check
  now consults the same prefix→node map the combo path uses, matching either
  spelling.
- New tests: steer-separation cases in `internal/semanticcache` and
  `internal/promptcache`; number-preservation and byte-stability cases on the
  strip; prefix-spelling cases on both matchers; and two integration cases —
  the reviewer's live probe (two requests differing only in steer, caches
  armed, excluded upstream must receive nothing, and an identical repeat
  still hits) and the node-prefix single-model exclusion. Each fix was also
  proven failing-on-prefix by reverting it in place and re-running the case.