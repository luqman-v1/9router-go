### The storage card's buttons work again on an install without a login

- **The symptom**: pressing *Prune Usage History* in Settings → Storage answered
  `Failed to prune usage history: Unauthorized: admin session or CLI token
  required`. The same gate refused *Prune Expired Records* and *Compact Database
  (VACUUM)*, and the only way to run them was a `curl` carrying the CLI token.
- **The cause**: `POST /api/settings/database/prune`, `.../prune-usage` and
  `.../vacuum` are on the always-protected list, because they delete rows. That
  gate admits a dashboard session cookie or the CLI token, and refuses everything
  else — including the case where login is switched off, in which the SPA
  authenticates with *nothing* because there is nothing to authenticate against.
  The dashboard's own buttons could therefore never succeed on the default local
  install. `internal/middleware/dashboard_auth.go` now admits the dashboard
  operator for those three paths when `requireLogin` is off, which is the trust
  the rest of the dashboard API already extends to an open install (where
  anyone reaching it can delete every API key and every connection).
- **The gate is unchanged where it matters.** An install that *does* require a
  login still needs a session cookie or the CLI token, and a client API key is
  still refused on all three routes. The backup download
  (`GET /api/settings/database`) is deliberately **not** in the exemption: it
  hands out the whole database, keys included, and keeps requiring the session
  or the CLI token.
- **Verified** by driving a real instance: with `requireLogin=false` the three
  maintenance calls answer 200 with no credential, and the export still answers
  401; with `requireLogin=true` the three answer 401 without a credential, 200
  with the CLI token, and 401 with a client API key. The new integration test
  was proven RED against the pre-fix gate (`prune-usage dry run without an admin
  credential = 401`) and GREEN with it.
- **The quota invariant still holds on the path the dashboard takes.** A prune
  deletes billing rows, and the spend in those rows is what a per-key
  `tokenQuota` is measured from, so a prune that dropped the spend would refund
  an exhausted key. `TestAnonymousPruneStillCarriesKeyQuota` pins that a
  credential-less prune still carries the spend: `usedTokens` is identical
  before and after, and the key is still refused with 403.
- No frontend change was needed: the paths were never 401-ing on an open
  install, they were being refused by the gate.
