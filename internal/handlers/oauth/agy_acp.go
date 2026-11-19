package oauth

import "net/http"

// agy-acp signs in with the same Google consumer OAuth client as antigravity
// (the agy CLI accepts those tokens), but stores its own connection rows:
// provider isolation means an agy-acp login never writes an antigravity row,
// and vice versa. Sharing credentials between the two providers stays an
// explicit per-connection sourceConnection reference.

// HandleAGYACPAuthorize returns the Google OAuth authorization URL for agy-acp.
// GET /api/oauth/agy-acp/authorize
func (h *OAuthHandler) HandleAGYACPAuthorize(w http.ResponseWriter, r *http.Request) {
	writeGoogleAuthorize(w, r)
}

// HandleAGYACPExchange exchanges a Google authorization code for tokens and
// stores an agy-acp connection carrying its own accessToken/refreshToken.
// POST /api/oauth/agy-acp/exchange
func (h *OAuthHandler) HandleAGYACPExchange(w http.ResponseWriter, r *http.Request) {
	h.exchangeGoogleCode(w, r, googleLoginTarget{
		provider:   "agy-acp",
		idPrefix:   "agcpy-",
		namePrefix: "Antigravity CLI",
		projectID:  defaultAntigravityProjectID,
	})
}
