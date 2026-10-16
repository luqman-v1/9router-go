### feat(gemini-web): route gemini.google.com directly, with no browser and no credential

Makes `gemini-web` routable for the first time. It was catalogued with a `501`
refusal executor because `gemini.google.com` has no published API, and the
upstream implementation reaches it by launching a real Chromium and typing into
the composer. That conclusion was half right: there is no API, but there **is**
an HTTP surface, and it answers a plain client.

**Why this is possible, and what was verified rather than assumed.** Live probes
against the site established three things, and every one of them was checked
before a line of the executor was written:

- `GET /app` hands an ordinary `net/http` client the session tokens
  (`cfb2h`, `FdrFJe`) in the inline `WIZ_global_data` object. No browser, no
  cookies, no TLS impersonation, no `uTLS` dependency.
- The same `StreamGenerate` endpoint the page's own JavaScript posts to answers
  a chat turn **anonymously** — zero cookies, zero credentials. The single-binary
  CGO-free constraint that killed the browser path was never the obstacle.
- The response is Google's `batchexecute` line protocol, in which **every frame
  re-sends the whole answer so far** rather than a delta. Deltas are therefore
  derived, not forwarded; a frame that is not a superset of what was already
  streamed is refused rather than papered over.

**Consequence for the provider entry:** `gemini-web` is now `NoAuth` with a
default key, because the site serves an anonymous visitor. Requiring a pasted
cookie would have made a plug-and-play provider into one that is not. A
`__Secure-1PSID` cookie is still honored when one is configured — what it buys is
the **choice of model**, not reachability: an anonymous session is served
Flash-Lite only, a signed-in one several models.

**Model selection is refused before the turn, not substituted after it.** The
site answers a model it does not recognize by silently answering with a different
one under the requested name. So the live `otAQ7b` registry is consulted per turn,
the request is refused with a `400` naming what the session *does* offer, and the
identity stamped on the answer is checked against the model that was resolved. A
Flash-Lite answer can never be filed under a Pro model's name.

**Also in this change:**

- Streaming SSE with real deltas, `reasoning_content` from the site's thinking
  channel, and the site's inlined ```` ```python?code_reference ```` sandbox
  trace moved out of the content channel into the reasoning one, where a coding
  client will not read it as the reply.
- Tool calling: the site has no native tool channel, so declarations are rendered
  into the prompt and calls are recovered from the whole answer, reusing the
  parser `deepseek-web` already carries rather than forking a second one.
- `x-goog-ext-525001261-jspb`, the header that names the model, is built per
  model from the registry's capacity field; the wrong shape is not rejected, it
  produces a different model's answer.
- Timeouts are two clocks, not one: 60s to the first byte and 90s between frames.
  A long think is not a failure, a stalled answer is, and a single total deadline
  cannot tell them apart.

**Bugs found by running real turns, not by the tests.** The unit suite was green
throughout all four of these:

- The site's "unauthenticated" code is `1016`, and `1016` also occurs as an
  ordinary constant **inside a healthy model registry**. Scanning the payload for
  it reported every working anonymous session as a refused one.
- The first correction — asserting that `1016` sits at index 14 — was wrong for
  the same reason: index 14 carries that constant in a healthy payload too. The
  detection is now on content, because a model list is the only proof of a usable
  session. That took a live call to a real upstream to see.
- Streamed chunks carried `"created":0`, which some clients reject outright,
  because the emitter never stamped the field.
- The streaming path never wrote `req.ResponseBuf`, so a streamed turn was
  recorded with an empty logged body and a zero token estimate — the same trail a
  model that answered nothing leaves. Every other streaming executor in the
  package writes it; this one did not.

**Fingerprint fidelity: the request is made to look like Chrome's.** The provider
does not use `net/http`. It dials through `bogdanfinn/tls-client` with the
`Chrome_150_PSK` uTLS profile and `fhttp`, which gives three things `net/http`
cannot:

- **TLS ClientHello** — Chrome's cipher suites and extension order (JA3/JA4),
  with per-connection extension shuffling as Chrome does it.
- **Header order** — Chrome writes request headers in a fixed non-alphabetical
  order; Go's `net/http` sorts them alphabetically before writing. That was
  verified on a raw socket rather than assumed. `fhttp` honours a `HeaderOrderKey`,
  so the navigation and XHR orders are now replayed explicitly, along with
  Chrome's HTTP/2 pseudo-header order.
- **Client hints that agree with the UA** — `sec-ch-ua` / `sec-ch-ua-platform`
  matched to the UA string, and a `sec-fetch-*` set per request kind:
  `navigate`/`document` for the bootstrap page, `same-origin`/`cors`/`empty` for
  the API calls.

**An earlier refusal of this dependency was wrong.** It was justified by a
"CGO-free, no new Go dependency" constraint that does not bar a pure-Go TLS
library, and that was asserted rather than tested. Measured afterwards: the stack
is pure Go, builds with `CGO_ENABLED=0`, cross-compiles to all five release
targets, and costs **+8.25 MiB** on the binary. The "uTLS alone, +2.18 MiB" middle
option was also identified — it gets the TLS fingerprint without the header order,
which is why it was not the one taken.

**Proxy pools still apply.** The chat path resolves a connection's pool into the
`http.Client`'s transport before the executor runs. The uTLS client is a different
stack and builds its own connection, so the proxy is read back off that transport
and handed to `tls-client`. Without that the request would leave from the host IP
while the dashboard still showed it as proxied — publishing the operator's egress
address, which is the one thing a pool was assigned to prevent. There is a test
for it.

**Verification:****Verification:**

- Unit suite: 17 executor tests over an `httptest` fake that serves the whole
  turn (bootstrap page, registry RPC, stream), pinning the call sequence, delta
  derivation, tool-call recovery, and each status the router acts on
  (401 stale cookie, 400 unavailable model, 502 substituted model / truncated
  stream / missing session tokens), plus the response-buffer and `created`
  regressions above — each was confirmed to fail before its fix. One of the 13
  pins the browser header set and two pin the ordering (Chrome's header order, and
  the HTTP/2 pseudo-header order), so a refactor that drops the client hints or
  falls back to `net/http` fails rather than silently sending a request that looks
  less like a browser. One asserts the proxy reaches the TLS stack.
- Live: a turn against the real site through the uTLS stack returned its answer,
  and the five release cross-compile targets all build with `CGO_ENABLED=0`.
- Dashboard probe tests: a supplied cookie is probed against the registry RPC, a
  refused one reports the advice, an empty one is reported as still working, and
  the `1016` regression is pinned.
- Catalog tests: `NoAuth` + default key, and the capability override that stops
  the web surface advertising the API surface's vision/audio/video/search.
- **End-to-end through the real gateway:** `POST /v1/chat/completions` with
  `gweb/gemini-3.1-flash-lite` returned `{"content":"gateway-ok"}`, streaming
  returned proper `chat.completion.chunk` deltas ending in `data: [DONE]`, an
  unavailable model returned the actionable `400`, and `gemini/…` still resolved
  to its own provider (AGENTS.md §3 isolation).

`gofmt`, `go vet`, the full `go test ./...` suite, `make vet-svelte` (83 errors,
at baseline, 0 unresolved) and `bun run build` all pass.

**AGENTS.md gains §4.F (Dependencies & Portability).** The guideline had no
dependency or portability rule at all, which is how an unverified "no new Go
dependency" claim got treated as policy in the first place. The new section is
written as general policy rather than as a note about this change, so it carries
no provider names, no library names and no measured figures — those live here, in
the fragment, where a project-specific record belongs.

What it states: a dependency is judged by two conditions and not by how heavy it
looks, and both are cheap to check before the dependency is proposed rather than
after — every `CROSS_TARGETS` pair still builds with cgo disabled, and
`go mod tidy` leaves a coherent graph with what the repository imports listed as a
direct require. It records that a *build* claim must be verified by building,
because an unverified "impossible" discards a working approach and the redesign is
paid for later. It asks for a measured cost rather than an assumed one when a
heavier and a lighter variant exist. It forbids cgo and per-platform build tags
outright, and it notes the prerequisite a first reader would trip on: `web/embed.go`
embeds `web/dist`, so the loop needs `make web-build` first and a fresh clone fails
there for an unrelated reason. The loop reads its target list out of the `Makefile`
instead of restating it, so a target added there cannot be forgotten. The same loop
is referenced from §7.5 (verify) and §8 (cheat-sheet).

**Supersedes** the gemini-web "catalog-only, needs a browser" notes in
`internal/proxy/executor/gemini_web.go`, `internal/providers/providers.go`,
`internal/proxy/executor/init.go`, `internal/handlers/dashboard/validate_web_providers.go`
and `web/src/lib/providers.ts`.

### feat(gemini-web): catalogue both registry naming forms, including Argon

The catalogue published only the versioned spelling of each model, so the names
the site's own registry produces could not be requested. The registry names a
model twice: by **category** — `Pro`, `Flash`, `Argon` — and by a **display label**
carrying the version — `3.1 Pro`, `4 Argon`. A client written against the site's
naming asks for `gemini-argon` or `gemini-4-argon`; the catalogue offered neither,
so a request for Argon was answered "cannot be served by this session" on an
account that has it. Both forms are now catalogued for every model the registry
reports, so `gemini-pro` / `gemini-3.1-pro` and `gemini-argon` / `gemini-4-argon`
each resolve.

**The category form is the one to keep, and the reason is release churn.** A version
prefix moves with every release — this registry already reports `3.5 Flash-Lite`
where the catalogue said `3.1` — while the category does not. Listing the category
name means the model stays reachable across a version bump without an edit here;
the versioned entries are kept because they are what existing clients send.

**A catalogued name that matches nothing is a trap, so it is tested.** A name added
to the catalogue without the matcher learning about it reaches the user as
"cannot be served by this session" even when the account has the model, which reads
as a broken provider rather than a mislabelled one. One test resolves every
catalogue entry against the fullest registry the site is known to report, and
fails when an entry matches nothing.

**Verified**: the new capability rows are `gemini-pro`, `gemini-flash`,
`gemini-flash-lite`, `gemini-argon`, `gemini-3-pro` and `gemini-4-argon`, each
carrying the web surface's own capability set (`Tools`, `Reasoning`,
`gemini-level`, thinking not disablable) rather than falling through to the generic
`*gemini-3*` pattern row, which would advertise the API surface's vision and search
flags. The three new executor tests pass, as does the whole `internal/providers`,
`internal/handlers/dashboard` and `internal/proxy/executor` suite. Argon itself
cannot be exercised end to end here: it is served to a signed-in account only, and
an anonymous session reports exactly one model (`Flash-Lite`), which is what the
live `otAQ7b` probe returned.

**The dashboard needed a second, separate edit, and that was found by running the
thing rather than by the tests.** The catalogue reaches the dashboard from two
places: the Go catalog feeds `/v1/models` and `/api/models/caps`, and
`ProviderDetailView` renders its "Available Models" rows from
`getModelsByProviderId()` in `web/src/lib/models.ts`. Both already answered with
nine names, so every check passed while the provider page still showed three — a
model the gateway would route and resolve was invisible in the UI that exists to
configure it. Same failure shape as the `1016` false positive above: the layer
under test was correct and the layer beside it was not.

`models.ts` declares itself auto-generated from the upstream registry and has no
generator in this repository, and nothing compares the two lists, so this drift is
silent by construction. The file is not redundant — it carries 23 media providers
the Go catalog does not — so it cannot simply be derived from the Go side; the
parity that matters is per provider, and for this one it now holds 9 for 9 in both
directions. Editing it by hand is correct but unguarded, and a test that pins this
provider's two lists together would be the place to stop the next one from
escaping the same way.

**Verified in the running dashboard**, not only in the bundle: the provider page
lists `gweb/gemini-argon` and `gweb/gemini-4-argon` beside the existing entries,
the heading reads "Gemini Web" with no "(Free)", and the button counts "Check All
Models (9)". The served asset contains `gemini-argon` and no occurrence of the old
label.

**Also removed the word "free" from the provider's name and messages.** The provider
card read "Gemini Web (Free)", and the refusal text explained that an anonymous
session reaches "the free model". Neither the plan nor the site's own naming calls
it that, and the label made a paid-tier-capable provider look like a free tier.
The card is now "Gemini Web" and the messages say what is true — an anonymous
session reaches **one model** — without a tier claim that changes under the user's
feet.

### fix(gemini-web): stop sending the router's placeholder as a cookie

`NoAuth` providers are dispatched with their catalog `DefaultAPIKey`, so the
executor received the literal word `anonymous` (or `public`) as `req.APIKey` even
when nobody had configured anything. The cookie renderer treated it as a bare
`__Secure-1PSID` value and sent `Cookie: __Secure-1PSID=anonymous` to
`gemini.google.com` on every request — a credential the user never supplied,
handed to a third party, and one that made the session look credentialed.

Found from a user report, not by a test: a request for `gemini-flash` was refused
with advice naming a "configured `__Secure-1PSID`" on a connection whose cookie
field was empty. The visible half was the wrong sentence; the invisible half was
that Google had been receiving a cookie nobody set.

Two placeholders are now recognised and dropped, matching on the whole value
rather than a substring — a real cookie whose value merely contains the word
`anonymous` is a credential to pass through, and dropping it would turn a
signed-in session anonymous, which is the same bug pointing the other way. Both
directions are pinned by tests.

The same change makes a refusal name the credential it actually tried, because one
message cannot be right for both cases: telling someone with a stale cookie that
"an anonymous session reaches one model" sends them looking for the cookie they
already pasted, when the fix is to replace it. With no credential the advice says
how to obtain one; with one configured it says to paste a fresh one.

### feat(gemini-web): post the signed-in page's SNlM0e token as `at`

The reference implementation for this endpoint posts `at` alongside `f.req`, and
treats its absence on a signed-in page as an expired session —
`could not extract SNlM0e (at token); session may be expired`, status 401. This
executor did not post it at all, so every request carried the shape an anonymous
visitor sends.

That survived review because an anonymous page genuinely omits `SNlM0e` — a live
fetch of `/app` without a cookie returns `cfb2h` and `FdrFJe` and no `at` — and an
anonymous session is the only one that had been exercised end to end. A signed-in
session is exactly the case the token exists for, and its absence was the one
difference between this executor's request and the request the page's own
JavaScript makes. Reported from a user about to use a signed-in account, not
caught here.

The bootstrap now extracts `SNlM0e` opportunistically rather than requiring it:
it must stay optional because an anonymous page omits it, so both cases keep the
shape they had. When a cookie is configured and the page still carries no token,
the site served the visitor's page, which is logged once per session instead of
surfacing later as a model refusal that blames the model.

**Verified**: two tests pin the field — a page carrying `SNlM0e` and a page
without it — over both RPCs an authenticated session uses. The first was confirmed
to fail when the token is dropped from the form, so it catches the regression it
describes rather than passing vacuously.

### feat(gemini-web): sign the registry call with SAPISIDHASH

The reference implementation sends `authorization: SAPISIDHASH <seconds>_<sha1>`
on the `batchexecute` call, and this executor did not. It is now sent when the
credential carries a `SAPISID` to sign with.

**What it is not:** a measured requirement. Probing the real endpoint with a
signed-in cookie showed the signature is **not** what decides the served set — the
same four models (Flash-Lite, Flash, Pro, Argon) come back with the header and
without it. The reasoning that produced it was plausible and wrong: that RPC does
answer *which models may this session use*, so it looked like the call the site
judges the caller on, but nothing demonstrated that a missing signature changes
the answer. It is kept because it makes the request match the one the page's own
JavaScript makes, which is the standard this provider holds itself to — and it is
written up this way so the next reader does not inherit a claim that was never
checked.

By contrast the `at` field above **is** a measured requirement: the same probe
without it answers `400`, and with it `200`.

The signature is `<unix-seconds>_<sha1("<seconds> <SAPISID> <origin>")>`, built
only when a `SAPISID` is present. An anonymous session has nothing to sign and
sends no header at all, because a malformed one is worse than none. The cookie
lookup matches the **whole** cookie name rather than a prefix: `__Secure-1PSID` is
a prefix of `__Secure-1PSIDTS`, and signing with the wrong value produces a header
the site rejects without a diagnostic.

**Verified**: three tests pin the signature, the anonymous case, and the
prefix-collision case. The first recomputes the digest from the request rather
than trusting the prefix, and was confirmed to fail when the header is dropped.

### feat(gemini-web): verified end to end against a signed-in Gemini Pro account

Every signed-in claim above was checked against a real account, which is the
verification this provider had been missing — the anonymous path was the only one
ever exercised before.

| Check | Result |
|---|---|
| `GET /app` with the cookie | `200`, and the page carries **`SNlM0e`** (an anonymous page does not) |
| Registry `otAQ7b` with the cookie | 4 models: `Flash-Lite`, `Flash` (3.8), `Pro` (3.1), **`Argon` (4 Argon)** |
| `gemini-web/gemini-argon` through the gateway | `200`, answer returned |
| `gemini-web/gemini-4-argon` | `200`, answer returned |
| Streaming with Argon | real deltas, terminated by `data: [DONE]` |
| Registry without the `at` field | **`400`** — the token is load-bearing |
| Registry without the signature | `200`, same 4 models — it is not |
| Credential in logs | absent; the dashboard API returns `apiKeyMasked` only |

**One consequence to know about.** `otAQ7b` names models by *category*, and a
category is stable across versions: this account's `Flash` entry carries the label
`3.8 Flash`, while the catalogue also publishes `gemini-3.7-flash`. A request for
the versioned name resolves to that same entry and is served by it, so the answer
comes back labelled `gemini-3.7-flash` although `3.8` produced it. The identity
check compares the registry **id**, which is what the site reports, so it cannot
see a difference between two catalogue names that point at one entry. That is the
category-first design working as intended — the alternative is refusing a name the
account can in fact serve — but a version literal in a model id is not a promise
about the version that answers.

**Cookie rotation is not implemented.** Google rotates `SIDCC` and the
`__Secure-*PSIDCC` family on the calls this provider makes and returns them as
`Set-Cookie`; the reference implementation rotates `__Secure-1PSIDTS` and `SIDCC`
explicitly through `RotateCookies` and persists the result. This executor uses a
cookie jar per session, so within one process lifetime those refreshed values are
used — a long-running instance rides out a `SIDCC` rotation — but a **restart
reloads the pasted credential**, and any rotated value is lost. A cookie can
therefore stop working before its own expiry date, and the remedy at that point is
to paste a fresh one. Rotation is the next step for this provider, not part of
this change.
