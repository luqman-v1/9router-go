### feat(persona): an injection tester that diffs the body and, optionally, shows the reply

The Persona page could tell you what the resolver *decided* — which preset won for
one id, and why — but not what that decision *does*. A new tester answers the
second question, and `Body` mode answers it with no upstream call at all.

- **Body (default, zero cost).** `POST /api/jailbreak/preview` builds the exact
  outbound body under the picked key's scope, and `JailbreakTester.svelte` diffs
  it word by word against what the operator typed. The injected prompt shows as a
  message "only in the body the gateway sends", with the operator's own turn
  still marked unchanged beside it — an addition, not a replacement. The base is
  built in the browser rather than fetched: the gateway has no
  "exclude every preset" preview, and synthesising one server-side would need a
  route whose answer must ignore the rules the rest of the feature exists to
  apply. An id nothing resolves for says so plainly instead of showing an empty
  panel.
- **Answer (one call).** The Pro edition's Injection Tester, ported: the prompt
  through `/v1/chat/completions` with the operator's key, the raw reply with a
  refusal-phrase verdict (`ANSWERED` / `REFUSED` / `UNCLEAR`), latency and total
  tokens. The same four probe chips, and the same refusal vocabulary including
  Indonesian. `UNCLEAR` is the honest verdict for an empty body, a reasoning-only
  answer or a provider error envelope — reporting those as `ANSWERED` would let a
  broken call read as a successful injection.
  The call sends the key's bearer token, not the dashboard session: `/v1/*` is
  the public lane and authorizes on that token, so a call sent with
  `credentials: 'include'` and no `Authorization` header got a 401. A key whose
  secret is not revealable cannot authenticate it, and the panel says which of
  the two it is looking at instead of relaying the gateway's auth error.
- **Scope (zero cost).** The same model and prompt under both scopes, resolved
  side by side by the resolver. This is the comparison the jailbreak page
  actually needs, and it is what the Pro edition's `VS` mode is not: Pro compares
  two *models*, which answers a question about the models rather than about the
  injection. Both columns come from `POST /api/jailbreak/preview` — once plain,
  once under the key — so the scope difference is visible without a model ever
  running. It has to be built that way: `/v1` authenticates on the presenting
  key and the resolver reads the scope off that same key, so two live calls with
  one key resolve identically, and two live calls with two keys would compare
  two *keys* rather than two scopes. A key with no override of its own shows no
  difference, and the header says so rather than implying a broken panel.
- Only the Answer mode spends tokens and depends on sampling. The copy says so
  where it introduces the three modes, so the operator knows which ones to reach
  for when the question is about the injection and which are about the model.

`web/src/lib/injectionDiff.ts` holds the pure logic, so each claim is testable
without a browser: an LCS word diff anchored on the shared tail before it falls
back to positional matching (a positional diff pairs the operator's turn with the
  injected prompt and reports the whole conversation changed — the panel would be
  useless), a coarse fallback above a token ceiling so a 512 KB prompt cannot hang
  the tab, and the refusal heuristic over the reply's opening.

Verified: `bun test src scripts` (915 pass, 0 fail — 19 over the diff and
verdict helpers, plus the three that pin what the live gate found: a failed
request never returns `ANSWERED` whatever its error text opens with, an empty
choice list and an unreadable body are `failed` rather than answered, and the
default argument keeps a plain call's verdict). `bun run ratchet:svelte`
(0 unresolved identifiers, baseline held at 74), `bunx oxlint
--deny-warnings`, `bun run build`, `go build ./...`.
`bun run e2e` at 80 pass / 0 fail across 23 files, including the new
`web/e2e/personaTester.test.ts` which drives the real gateway and Chromium: the
injected prompt reaches the DOM as a message only in the body the gateway sends,
the operator's own turn stays marked unchanged beside it, the header counts what
changed rather than reporting "No difference", and an unarmed id reads as
"Nothing resolved for this id under this scope". The two `aria-label`s this added
— `Preset prompt` and `Prompt to test` — also stop the new page from colliding
with the preset modal's own prompt field, which is what broke the per-key editor
test the first time the two shared a page.
Driven by hand against a gateway on :20131 as well, which is where two of the
above came from. The first browser run showed both live cards reading
`ANSWERED` under a `request failed: Authentication required` note — the verdict
was computed from the error text. The second showed the Scope cards rendering
that same 401 twice, because a live call with no `Authorization` header cannot
reach the resolver at all. The `UNCLEAR` verdict and the token now carry
`bg-warning/15` over `text-text-main`, because `--color-warning` measures
2.06:1 against a light surface and 4.91:1 over its own blend, which cannot carry
10px text in either theme; the wash stays as the signal and the label uses the
theme foreground (11.6:1 dark, 15.4:1 light).
