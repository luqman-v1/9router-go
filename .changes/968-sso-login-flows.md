### feat(auth): serve the OIDC and SAML login flows, and make the local password reset fail-closed

The dashboard linked to `/api/auth/oidc/{start,callback}` and `/api/auth/saml/{start,acs,metadata}`, but none of them existed: pressing "Sign in with SSO" on a configured install answered 404, so an operator with `requireLogin` on had no way in. This adds the flows and the recovery path, then closes the fail-open shapes a security review found in the first cut of them.

**SSO login flows**
- OIDC: Authorization Code + PKCE (S256), with state, nonce and the code verifier carried in short-lived `HttpOnly` cookies so the callback validates what it started. RS256 only; `alg: none` is refused before any key lookup.
- SAML: AuthnRequest generation, POST-binding ACS, and SP metadata, with the assertion signature verified through `goxmldsig` against the configured IdP certificate.
- The browser flows and SP metadata are mounted in `SetupServerRouter`, outside `RequireDashboardAuth` — they exist precisely for the case where no session exists. The `/api/auth/{oidc,saml}/test` settings probes stay in the dashboard group, because they echo the IdP configuration.
- `TestSetupServerRouter_SSOLoginRoutesArePublic` asserts each route by identity (an `<md:EntityDescriptor` root, a `login?error=oidc_not_configured` redirect), not by "not 401" — a no-op handler or a deleted registration passes a status-only assertion, which is exactly the regression the test is for.

**Fail-closed fixes**
- **OIDC claims**: `iss` and `exp` are now required. They were optional, so a token carrying neither was accepted; OIDC Core requires both.
- **`redirect_uri` origin**: `publicOrigin` no longer trusts `X-Forwarded-Host` or `X-Forwarded-Proto`. Those are the headers an attacker in front of an unvalidating reverse proxy controls, and the value becomes the OIDC `redirect_uri` / SAML ACS URL — a forged one delivers the authorization code to the attacker. A proxied deployment must now set `BASE_URL`; without it the flow refuses to start with `oidc_base_url_required` / `saml_base_url_required` rather than silently building a wrong origin.
- **SAML `AudienceRestriction`**: an assertion with no `<Audience>` is rejected (SAML Core §2.5.1.4). Previously the check returned success on an empty set, so the signature alone authorised a session at any SP sharing the IdP signing key. `InResponseTo` is fail-closed for the same reason.
- **Signature wrapping**: `TestVerifySAMLAssertionResistsSignatureWrapping` pins that a document with an unsigned assertion added alongside the genuine, signed one cannot open a session for the attacker's identity.
- **Password reset**: still loopback-only on `RemoteAddr` (never the `Host` header) and same-origin, and now also demands `{"confirm":true}` and is rate-limited per peer address. A same-host tunnel forwards strangers as `127.0.0.1`, and a bare POST — a cross-site form, a prefetch, a stray curl — must not clear the credential.
- `nodeRequestIsLocal` inspects `r.RemoteAddr` only. The `Host` header is client-controlled, and "Host: localhost" from a remote socket previously disabled both this gate and the `mustChangeDefaultPassword` check on login, giving full remote dashboard takeover; `TestHandleAuthLogin_RemoteDefaultPassword_HostSpoofingFails` pins it.

**Verified**: `go build ./...`, `go vet ./internal/handlers/...`, and `go test ./internal/handlers/... ` (sso, dashboard, router) all pass. Each security assertion was mutation-checked: reverting the specific guard turns the corresponding test red.
