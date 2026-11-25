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

Two properties the first cut missed, both fixed here:

- **`tokenVersion` is read-only over the API.** It is not in `protectedSettingKeys`,
  so any session could `PUT {"tokenVersion":0}` and resurrect every token a
  password change revoked. The protected-key check now runs **before** anything
  is applied, so a payload carrying both `newPassword` and `tokenVersion` is
  refused whole instead of changing the password and then reporting 400.
- **An SSO operator stays an SSO operator after changing the password.** The
  re-issued cookie is signed from the caller's existing claim set rather than as
  a bare password session, so an OIDC/SAML operator does not silently become a
  "Password user" in the dashboard. The claims are read before the change: the
  change bumps the epoch, which invalidates the very token they come from.

The reset and setup paths are now covered in-process too. `POST
/api/auth/reset-password` clears the stored hash and `POST /api/setup` writes
the first one; neither goes through `changeDashboardPassword`, so the
in-memory fingerprint stayed stale until the next restart — the same
failure mode this PR closes, reached through a different write. Both handlers
now reinstall the epoch from the store (`auth.LoadSessionEpoch`) right after
the settings write: a token minted before a reset stops verifying the moment
the reset lands, and the session the setup wizard hands back verifies against
the hash it just wrote. Tests pin both, and one fails without the refresh.
