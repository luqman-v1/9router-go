### feat(providers): free-pi — session-as-API for the ad-funded `pi` agent

New provider `free-pi` (`fp`), turning free-pi's CLI-only account into a
multi-client API behind this gateway. Upstream is plain [OI] Chat Completions at
`https://api.freepi.ai/v1/chat/completions` behind a Bearer JWT, so the transport
is ordinary; what is not ordinary is **session identity** and **advertising**.

**Session identity.** Every completion must carry a stable `x-session-id`, and the
account tolerates a bounded number of concurrent sessions (4, per
`hungkevin/free-pi-cli-proxy` `server.mjs:958`). Exceeding it yields
`409 concurrent_session`. This builds for the tightest reading of that limit —
**one session per account** — and leaves the other three unspent:

- The id is *derived*, not minted: `uuid.NewSHA1(namespace, accountKey)` where
  `accountKey` is the JWT `sub`. Two dashboard connections for one account, a
  restarted process, and a second gateway instance therefore all converge on the
  same upstream session, so nothing has to be remembered for that to hold.
- A lease in the existing `upstream_leases` table (`LeaseScopeFreePiSession`)
  records which process is currently driving the account. Its value is a
  **per-process epoch**, deliberately not the session id: every holder derives
  the same session id, so a session-id value would match for everybody and make
  the compare-and-delete that guards releases a no-op.
- On 409 the gateway resets once (`POST /session/reset`, the CLI's
  `/close-other-session`) and retries once — but only when no sibling holds the
  lease, at most once per per-account cooldown, and not at all when
  `freePiAutoReset: false` is set on the connection. The same reset can end a
  human's running `free-pi-cli`, so the opt-out exists for exactly that case.

**Advertising.** Upstream checks ad *impression records*, and the reference proxy
satisfies that by posting `imp_fired: true` every ten minutes for ads nobody saw.
That misreports to the advertiser whose spend funds the inference, so it is not
implemented that way here. Instead:

- `GET /api/free-pi/ad` serves a banner creative and records what it handed out;
  serving an ad does **not** report an impression.
- The dashboard renders it (`FreePiAdBanner.svelte`) and, once the creative is
  actually painted, calls `POST /api/free-pi/ad/impression`. That endpoint only
  accepts the id of an ad this gateway served, and **consumes** it — one serving,
  one impression, no re-reporting, no timer.
- A `204` from the ad network is "nothing to show", not an error: the banner is
  simply absent, and no impression follows.
- An `imp_url` outside `trygravity.ai|com` is refused before it is fetched. The
  URL is upstream-supplied and fetched server-side, so an unvalidated host is an
  SSRF primitive; the reference applies the same check.

Deliberate, documented limitation: free-pi's protocol also carries an
`x-gravity-context` header, built by the npm `@gravity-ai/api` SDK from *browser*
signals (user-agent, screen size, timezone, locale) plus a Gravity pixel session.
It has no Go port, and a server cannot observe a screen size or a browser locale,
so generating it here would mean inventing exactly the signals this design
refuses to fake. It is therefore omitted from chat requests, which means whether
upstream still *attributes* an impression without it is unverified. The gate
itself is honoured; that attribution question is not answered, and the gap is
stated in `free_pi_ads.go` rather than hidden.

**Login** is free-pi's own GitHub device flow, so the CLI need not be installed:
`POST /api/oauth/free-pi/initiate` → `/auth/consent` → `/auth/token`. Consent is
never recorded on the user's behalf — the screen discloses training on prompts,
code and responses with no opt-out, so `initiate` refuses to contact upstream at
all until the request asserts this user accepted this exact version, and the text
itself is served from `GET /api/oauth/free-pi/consent` so the wording the user
reads cannot drift from the version recorded.

Also added:

- A **Clear session** action in the dashboard, wired to
  `POST /api/oauth/free-pi/session/reset` — the same operation free-pi-cli exposes
  as `/close-other-session`, surfaced as an explicit button because the same call
  also ends a human's running CLI session on that account.
- `GET /me/stats` quota mapping (credits, refill time, token counters). free-pi
  reports credits rather than dollars, so nothing is converted into a spend
  figure upstream does not disclose.
- 429 taxonomy, adopted from the reference proxy and mapped onto the existing
  rules: `rate_limit_exceeded` is an hourly cap (short fixed cooldown, rotation
  eligible) while `creditlimitexceeded` / `insufficient_quota` / `billing_error` /
  `budgetlimiterror` / `gousagelimiterror` mean the day is spent (long fixed
  cooldown, never retried on the same account).
- A dead credential is refused **before** the request goes out. free-pi issues a
  ~90-day JWT with **no refresh token**, so expiry is terminal; without this the
  generic 401 rule cooled the connection for two minutes and retried a
  permanently dead credential forever while the dashboard showed it healthy.
- The executor checks the upstream status itself and returns a real
  `*proxy.UpstreamError`. Registering on the default passthrough would have
  turned the 409 into an HTTP 200 carrying an error body, recorded in
  `usageHistory` as a served turn.

The wire contract is recorded in `internal/providers/testdata/freepi/`, one
fixture per response with its upstream source named, and pinned by
`TestFreePiContractInvariants` so a fixture cannot silently disagree with the
constants the code branches on.

A sign-in could hang forever, found on the first live run and fixed here:

- The real `POST /auth/token` answers `{"status":"pending"}` — no `code`, no
  `retry_after` — which is now parsed and pinned by a test against the three
  shapes actually observed upstream (`status: pending`, `403 consent_required`,
  and a token grant). An unparseable body is an error rather than "pending": that
  is how a stall becomes a spinner with no diagnostic.
- The pending window was 15 minutes, the *same* as the device code's own ~899s
  lifetime, so a first-time GitHub sign-in with 2FA could outlive it. It now
  honours the server's `expires_in` plus a minute, and is bounded at 20 minutes.
- An expired or unknown login answers **410 Gone**, not a bare 400, so the client
  can tell "start again" from "retry".
- The dashboard no longer swallows poll failures. It previously ignored every
  error, so any 410/502 rendered as an indefinite "Waiting for authorisation…"
  with no way out; it now shows the message, stops on 410 and offers the consent
  step again, and gives up after three consecutive transient failures.
- Each poll logs its outcome, so a stalled sign-in is visible in the terminal
  instead of looking like a dead endpoint.

Incidental: `internal/handlers/usage_chart_test.go` seeded a hardcoded
`2026-10-16` and then asked for a trailing 7-day window, so it began failing on
the calendar rather than on the code once real time passed it. The seed and the
all-period test's clock are now relative. Pre-existing rot, fixed here because it
made the surrounding suite unreadable.

Verified with `go test ./...`, `make test-integration` (including 409
reset-and-retry against the real router and database), `make vet-svelte`,
`bun run build`, and a dashboard parity check that the seeded model id appears in
both the Go catalog and `web/src/lib/models.ts`.
