### fix(deepseek-web): present the request as the site's own client, and make the credential refreshable

The `deepseek-web` provider was banned in practice: the request it sent was
not the request the site's own client sends, and the credential it used could
not be renewed without a DevTools ritual. Both are fixed, and the fixes are
based on a capture of the live client rather than on inference.

- **The header set was not the site's.** A capture of a real Chrome against
  `chat.deepseek.com` shows the site runs TWO header sets: the `x-client-*`
  block (bundle id, platform, version, locale, timezone offset) belongs to the
  settings and telemetry calls, while the chat calls — the token exchange, the
  proof-of-work challenge, the completion — carry no block at all, because the
  site issues them below the interceptor that adds it. The provider sent the
  block on every call, sent `sec-ch-ua*` client hints that no browser sends on
  an API call, and sent `origin` on the GETs, which the site does not. Each
  call now carries the set the site sends on that call. The header ORDER is
  the order Chrome wrote, replayed through the browser-fidelity transport,
  because Go's `net/http` sorts headers alphabetically and the order is part
  of the fingerprint.
- **The client version was stale.** The site ships `2.5.0`; the provider
  claimed `2.3.0` and answered a stale client with a toast telling the user
  to update. The version now matches the site's.
- **The timezone header had the wrong sign.** The site sends the offset EAST
  of UTC in seconds — 25200 on a UTC+7 machine. The provider negated Go's
  offset and so sent `-25200` for the same machine: the one value a real
  client can never send, and a free signal that the request did not come from
  a browser.
- **The cookie was fabricated.** The provider invented an anti-bot cookie
  (`HWWAFSESID`, `Hm_lvt_*`) and sent it on every call. The site's WAF reads a
  cookie the SITE issued, and a cookie it never issued is a request it does
  not recognise. The fabricated cookie is gone, and the site's own cookie is
  sent instead — measured: the document path answers HTTP 202 with a
  JavaScript challenge without the site's cookie and HTTP 200 with it, over
  Go's TLS stack and over Chrome alike, so the WAF decides on the cookie and
  not on the TLS fingerprint. The cookie is carried in the transport's jar, so
  the value the site last issued is the one on the wire, and it is stored
  sealed with the rest of the credential.
- **The user agent contradicted the TLS stack.** The UA claimed Chrome 150
  while the transport's ClientHello said Chrome 150 but the capture ran
  Chrome 156, and the client hints named a third platform. The UA and the TLS
  profile now name the same build, which is the newest Chrome the pure-Go
  TLS stack ships a profile for.
- **The error codes were misread.** The site's own bundle names them:
  40002 MISSING_TOKEN, 40003 INVALID_TOKEN, 40012 USER_IS_BANNED, 40029
  IP_ACCESS_RESTRICTED, 50006 MUTED. The provider read 40002 as a RATE
  LIMIT, so a request that carried no credential was answered with a 429,
  cooled the account down, and was retried against the same account instead
  of being reported as the dead credential it was. A missing or rejected
  credential is now a 401, a ban is a 403 (the account itself is refused, so
  cooling it down would only send the next turn to the same refusal), an
  address refusal is a 403 (the account is fine, the egress is not), and a
  mute is a 429. A banned account no longer loses its cached access token —
  the credential was never the problem.
- **The credential can be read out of a browser that is already signed in.**
  The site keeps the API's `userToken` in localStorage and the WAF's cookie
  in the cookie jar, and a browser the operator is already signed in to has
  both. `POST /api/providers/deepseek-web/import-chrome` reads them out of
  the profile: the token from the profile's localStorage, the cookies from
  the profile's cookie database (unsealed with the key the keyring holds).
  The dashboard's connection form has the button, and it says what the
  other way in costs: a browser that is running keeps the same session the
  router renews, and the two cancel each other out.
- **The credential can be refreshed.** The site's API authenticates with a
  `userToken` that only a real sign-in issues — there is no refresh token to
  exchange — and the WAF's cookie is renewed only by a browser running the
  WAF's JavaScript challenge. So a browser sign-in now captures both in one
  step: `POST /api/providers/deepseek-web/browser-login` opens a throwaway
  browser with a disposable profile, waits for the sign-in, reads the
  `userToken` out of localStorage and the site's cookies out of the jar, and
  deletes the profile on the way out. The dashboard's connection form has the
  button. Refreshing the credential is now a button instead of a DevTools
  ritual.
- **The credential is renewed on the connection, not just produced.** The
  sign-in above answers with a credential for the operator to paste.
  `POST /api/connections/{id}/renew-deepseek` writes it to the row instead:
  the browser opens, the operator signs in, and the row's `apiKey` and
  `deepSeekCookie` are replaced with what the browser produced. The rest of
  the row is left alone, so a rename or a priority change made while the
  browser was open is not reverted. The dashboard's per-connection Actions
  menu has the button.

Verified against the live site on 2026-10-22: a whole turn ran end to end
with the credential read out of a signed-in profile — the token exchange, the
session, the proof of work and the streamed completion all succeeded — and
the WAF was measured on the document path: with the site's own cookie it
answers HTTP 200 with the app, and without one it answers HTTP 202 with
`x-amzn-waf-action: challenge` and the challenge script.

The WAF's token is not renewable by a client, and that is measured rather
than assumed: a document load that presents a valid cookie is answered with
HTTP 200 and NO `Set-Cookie` at all, across three consecutive loads. The
site re-issues that token only to a browser that solved its challenge,
which is why the renewal drives a browser rather than running on a timer.

The header set, the order and the error codes come from a netlog capture of
the live client rather than from memory.
