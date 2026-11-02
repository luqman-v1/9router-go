### Kimchi dual auth is covered on both paths

Kimchi is a dual-auth provider: the dashboard renders an OAuth browser-token
login and a manual API-key button, both driven by the registry's `authModes`
(upstream `hasDualAuthModes`). Neither half had a test, so a provider that
rendered two buttons with only one working path would have shipped unnoticed.

The gap is now closed on the side that lives in Go:

- The registry declares both modes and is **not** `noAuth` — the dashboard hides
  the Connect card only on an explicit `noAuth` flag, so a `freeTier` category
  must not be read as "no credential needed".
- `POST /api/connections` with an `apiKey` stores that key and leaves the
  connection active, which is what the manual button produces.
- `GET /api/oauth/kimchi/authorize` points at `app.kimchi.dev/cli-auth` with
  `flowType: browser_token`, so the UI renders the paste-token form. A redirect
  to the wrong host sends the operator to a login page that cannot issue a token.
- `POST /api/oauth/kimchi/exchange` refuses an empty token with 400 before any
  network call, rather than surfacing a 502 from a failed upstream probe.

The OAuth half is deliberately not asserted through the create route: the
dashboard posts it to `/api/oauth/kimchi/exchange`, which stores the token under
both `apiKey` and `accessToken` so the probe and the chat router each find it.
Testing it through the create route would exercise a path nothing uses.
