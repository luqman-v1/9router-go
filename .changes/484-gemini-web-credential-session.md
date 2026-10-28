### fix(gemini-web): renew the session, and name the cookie that decides it

A signed-in account could not be told apart from an anonymous one, its image
was uploaded anonymously, and its session cookie was never renewed.

- **The image upload carried no session.** The upload goes to Google's
  content-push service — a different host from the site — and a cookie jar is
  per-host, so the site's cookies were not on the request. The upload belonged
  to no session, and the model cannot resolve an attachment that has none. The
  session's cookies are now copied to the push host, the way the site's own
  composer carries them, and only the Google session cookies are copied.
- **A credential that cannot reach the signed-in models was blamed on the
  wrong cookie.** The model registry is answered as an anonymous visitor
  unless the request carries `__Secure-1PSIDTS`, and that cookie is the one
  the site rotates every few minutes. A header carrying only
  `__Secure-1PSID` is served one model however fresh it is. The probe result,
  the quota panel and the refusal of an unreachable model now name that
  cookie, and the credential hint asks for the whole header including
  `__Secure-1PSIDTS`.
- **The session cookie was never renewed.** `__Secure-1PSIDTS` is the site's
  only source for a new one: a plain page load hands out `NID` and `COMPASS`
  and nothing else. A credential therefore fell back to one model within
  minutes and had to be pasted again. A background loop now renews it through
  the site's own rotation endpoints — the sentinel post, and the iframe page
  that carries the rotation id — and stores the renewed value back on the
  connection. The gap between two renewals is the one the site asks for, with
  a grace on top because the site answers a second call within the same
  minute with HTTP 429. A connection with no credential, or one whose
  credential carries no session timestamp, is left alone.

  The renewal and a browser that is still open do not get along: Google
  rotates the same session from both sides, and the two cancel each other
  out, which signs the account out. The credential therefore has to come from
  a session nothing else is rotating, and the dashboard now offers one: a
  button opens a browser with a throwaway profile of its own, reads the
  session out of it once the sign-in is done, then closes the browser and
  deletes the profile. Nothing is left running that could rotate the session,
  so the router's renewal is the only one. Copying the cookies from an
  Incognito window that is closed right after is the alternative, and the
  dashboard says so where the credential is pasted. The renewal can also be
  turned off entirely with `GEMINI_WEB_ROTATION=off`.
- **A session could only be pasted, never read.** The dashboard now offers
  two ways to get a session, and both are one click. The first opens a
  browser with a throwaway profile of its own, reads the session out of it
  once the sign-in is done, then closes the browser and deletes the profile.
  The second reads the session out of a browser that is already signed in:
  the browser's own cookie database is opened, the key that seals it is
  taken from the platform's secret store, and the cookies are unsealed.

  The second way is the convenient one and it carries a trade the first does
  not — a browser that is running keeps rotating the same session the router
  renews, and the two cancel each other out. The answer says so when it
  finds a browser that is running, and names the throwaway browser as the
  way to avoid it.

  The reading is a port of the reference bridge's
  (azrinaldi/gemini-stealth-bridge internal/cookies/chrome_sqlite.go) and
  covers the Chromium cookie database: the database is copied before it is
  read, because a browser writes to it while it runs; the key is taken from
  the login keyring, the macOS Keychain, or the profile's own "Local State"
  file on Windows; and the values are unsealed under either scheme a
  Chromium uses. A machine whose key cannot be read — a browser with
  App-Bound encryption, or a keyring that is locked — is told so plainly
  rather than being reported as a browser with no session.
- **A sealed credential was invisible to the connection list.** The
  single-row readers open the sealed columns before they hand a row back, but
  the list query did not, so every caller that read the credential from a
  listed row — the chat path's connection selection, the dashboard's
  credential check, the rotation loop — saw an empty credential. On an
  install with a vault configured, every request was dispatched without its
  key. The list reader hydrates now, the way the single-row readers do.

Measured live against `gemini.google.com` on 2026-10-21 with a real signed-in
cookie header: a header of `__Secure-1PSID` alone was served Flash-Lite only
and reported no quota, and the same header with `__Secure-1PSIDTS` added was
served Flash-Lite, Flash, Pro and Argon, reported the 5-hour and weekly
windows, and answered an `gemini-4-argon` turn. The site accepted the
unsigned registry call as well, so the signature is not what decides the
served set — the session timestamp is.

The upload is served by a second server in the test, so the test fails if the
copy to the push host is dropped. The renewal is tested against a stand-in
for the rotation endpoints, which live on another host and so cannot be
reached by a test that could not move them. A live suite was added for a
credential supplied through the environment; it is skipped without one.

Verified with `go build ./...`, `go test ./...`,
`go test -tags=integration ./internal/integration/...`,
`golangci-lint run --new-from-rev` (0 issues), `make vet-svelte` (0
unresolved identifiers, 81 errors against a baseline of 81) and the
portability gate over all five release targets. The anonymous turn and a
signed-in turn with an image were exercised live against `gemini.google.com`.

The renewal was exercised against the live endpoint on 2026-10-21, five times
in a row: four renewals a minute apart were answered with HTTP 200 and a new
cookie, and every renewed credential still reached the account's four
models. A renewal issued inside the same minute was answered with HTTP 429,
which is what the gap the worker uses is there to avoid — the worker renews
every nine minutes, so it stays well clear of that.

Those runs found a bug in the renewal itself: the site issues a fresh `NID`
on every answer, including a refusal, so counting "the site sent something"
as a renewal reported a refused renewal as a successful one — the caller
would store a credential that reaches one model and believe the session had
been renewed. A renewal now counts only when the cookie that carries the
session moved.

A live suite covers the renewal and the loop, along with a live tool call
and the round trip that follows it: the model called `get_weather` with
`{"city": "Jakarta"}`, and the second turn answered "The weather in Jakarta
right now is sunny with a temperature of 31°C" from the tool result the client
sent back.

The import was measured the same way. The browser's own cookie database on a
machine that runs Chrome was read through the new code: 38 Google cookies
were found, all 38 were unsealed, and all 38 carried the host binding that
Chrome 130 and later put in front of a value. The session that came out was
asked for the model registry and was served Flash-Lite, Flash, Pro and Argon
— the same four models a hand-pasted cookie header reaches, which is the
check that matters, because a session the site does not recognise is no use
to the router.

The key that opens the cookies came from the login keyring over the secret
service's D-Bus interface, because the machine that was measured on has the
keyring and not the `secret-tool` command that normally asks it. Both are
now tried, so a machine with either can be read.
