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