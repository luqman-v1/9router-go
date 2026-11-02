### fix(auth): serve the OIDC login flow the dashboard links to

The settings panel could already test an issuer and probe a client secret, but
the two browser routes `LoginView.svelte` links to —
`/api/auth/oidc/start` and `/api/auth/oidc/callback` — were never registered.
Pressing "Sign in with SSO" on a configured install answered 404, so an operator
who turned OIDC on had no way in.

**`start`** fetches discovery, generates a state, a nonce and a PKCE verifier,
stores the three in short-lived httpOnly cookies, and redirects to the
authorization endpoint with the S256 challenge.

**`callback`** checks the state against that cookie, exchanges the code at the
token endpoint, verifies the id_token, and opens the dashboard session with the
OIDC identity claims the session type already carried.

The verifier is written against the standard library rather than pulled in:
RS256 over a JWKS, then the claims that make a token mean what it says —
issuer, audience (string or array), expiry, not-before, and the nonce, which is
what binds the token to the request that started the flow. Only RS256 is
accepted, so a token that asks for an algorithm the code does not implement is
refused rather than accepted unverified — `alg: none` is the classic bypass and
is rejected before any key lookup.
