### Dashboard: a password change ends every existing session

- Sessions are stateless HS256 cookies, so nothing ended one early: a token
  captured before a password change kept working for the rest of its 24 hours.
  Tokens now carry `tv` (a revocation epoch stored in settings) and `ph` (a
  sha256 fingerprint of the stored password hash), and verify only while both
  still match. `ph` catches a password reset that rewrites the hash without
  bumping the epoch.
- A password change writes `password` and `tokenVersion` in the same settings
  update — so no request can observe a new password beside an old epoch — then
  re-issues the caller's cookie. The operator who changed the password stays
  signed in; every other session is signed out.
- Tokens minted before this change carry neither claim and are read as epoch 0,
  so deploying it logs nobody out. The epoch is loaded once at boot and
  refreshed on the change, so authorization adds no database read per request.
- The local CLI token (`x-9r-cli-token`) is a separate credential and is
  unaffected.
