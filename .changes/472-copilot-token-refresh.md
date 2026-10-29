### fix(media): refresh the short-lived Copilot token instead of sending an expired one

#460 made the Copilot token the credential the embeddings lane actually sends,
which made its expiry matter: the device login stored `copilotToken` and
`copilotTokenExpiresAt` together, and nothing ever acted on the second one. Once
the token expired, every embeddings request answered an upstream 401 that the
gateway could not recover from, even though the connection still held the OAuth
credential it could trade for a new one.

Four changes:

- **Refresh before sending.** The expiry is parsed (unix seconds, milliseconds,
  or an RFC 3339 stamp) and a token inside a two-minute skew is re-exchanged
  before the request is built, so the refusal is avoided rather than observed.
  An unreadable or missing expiry is treated as "unknown", not "expired": the
  token is sent, and the 401 path is what decides.
- **One retry on 401.** A 401 on an exchanged token re-runs the exchange once
  and retries. A second 401 reaches the caller — that is the upstream's answer
  about the account, not the token, and looping would trade the credential for
  nothing.
- **Persist through `UpdateConnectionData`.** The new token is written back with
  the same writer the background refresh loop uses, inside
  `providerSpecificData` — the object the connection reader hydrates. A full-row
  update would have written back the name/priority/isActive this request read
  and reverted a toggle the operator made in the meantime.
- **Store the expiry the login read.** `strVal` only accepts a string, and the
  Copilot endpoint answers `expires_at` as a unix second count, so every github
  row recorded an empty lifetime. `scalarString` renders numbers too, which is
  what makes the pre-flight check possible at all.

Verified with `TestCopilotTokenExpiry` (13 shapes), `TestStoredCopilotToken`
(fresh / expired / inside-skew / unknown), `TestRefreshCopilotToken_*` (exchange
headers, persisted keys, OAuth credential preserved, four failure shapes that
leave the row alone) and four handler tests against a stub exchange: an expired
token is refreshed before the first attempt (1 embeddings call, 0→1 exchange), a
401 is retried exactly once, a second 401 stops, and a fresh token costs no
exchange.

Against the pre-fix `media.go` the three defect tests fail with the reported
symptom — `embeddings Authorization = "Bearer copilot-stale"`, `exchange calls =
0`, `row not healed`, and `expected 200 after one retry, got 401` — while the
fresh-token control passes on both, so the tests measure the defect and not the
fixture.
