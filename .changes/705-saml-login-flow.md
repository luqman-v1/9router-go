### fix(auth): serve the SAML login flow, with the signature verified

The settings panel could test a SAML configuration and serve SP metadata, but
the two browser routes `LoginView.svelte` links to —
`/api/auth/saml/start` and `/api/auth/saml/acs` — were never registered, so
pressing "Sign in with SAML" answered 404.

**`start`** builds a deflated AuthnRequest for the redirect binding, stores its
ID in a short-lived cookie, and redirects to the IdP.

**`acs`** verifies the posted assertion and opens the dashboard session with the
SAML identity claims the session type already carried.

**The signature is verified with `goxmldsig`, not by hand.** XML Signature
Wrapping is the attack this flow exists to survive: an attacker who can move the
signed element inside the document makes a validator read a different assertion
from the one that was signed. A hand-rolled verifier is how that bug ships.
After verification the claims are read from the *validated* tree, never from the
raw document.

Beyond the signature: the validity window, the `InResponseTo` (which makes a
captured assertion unusable against a later login), and the
`AudienceRestriction` (which stops an assertion minted for another service from
opening a session here).

The new dependency is pure Go and was checked against §4.F before it was added:
`CGO_ENABLED=0` builds all five targets in `CROSS_TARGETS`, it is a direct
require after `go mod tidy`, and it costs three modules (132 → 135).
