package oauth

import (
	"net/http"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

// FreePiConsentText is free-pi's first-run consent screen, verbatim from
// dennisonbertram/free-pi-cli src/consent.ts.
//
// It is reproduced here rather than summarised because the user is being asked
// to accept exactly this, and a paraphrase would let them agree to something
// narrower than what upstream actually records. The dashboard fetches it from
// /api/oauth/free-pi/consent and renders it unmodified, so Go holds the single
// copy — there is no second string in the SPA to drift from this one.
//
// internal/providers/freepi_contract_test.go pins this against the recorded
// fixture in testdata/freepi/consent.json.
const FreePiConsentText = `free-pi — before you start

  * Inference is free. Ads pay for it, and so does training.
  * Ad cards render in the terminal UI. Some ads come from a third-party ad
    provider (Gravity), which receives excerpts of your recent conversation
    to pick a relevant ad.
  * Gravity also keeps a session and visitor ID on your machine (in
    ~/.free-pi) and receives usage events from free-pi to measure and
    attribute ads.
  * Your coding sessions (prompts, code, files, and model responses) are saved
    as traces and used to train models. We may share or sell that data to
    third parties. Once you accept, there is no opt-out, except where the law
    gives you one.
  * We do our best to remove personal information from traces before we store
    them. We cannot promise it is perfectly clean.
  * free-pi is in alpha and testing. There are no warranties or guarantees,
    expressed or implied, and no guarantee of uptime.
  * If GitHub email capture is enabled, we collect your GitHub email and may
    use it to contact you about the service or product.
  * free-pi-cli auto-updates itself when a new version is available. Disable
    with FREEPI_NO_AUTO_UPDATE or {"autoUpdate":false} in ~/.free-pi/config.json.

By accepting you agree to the Terms of Service (https://freepi.ai/terms)
and the Privacy Policy (https://freepi.ai/privacy).

Decline and nothing is sent to the free-pi server.`

// HandleFreePiConsent exposes the consent screen to the dashboard.
//
// GET /api/oauth/free-pi/consent
//
// It is served from the backend so the text the user reads and the text that was
// recorded cannot diverge, and so a wording change ships with the gate that
// enforces it rather than with a frontend build.
func (h *OAuthHandler) HandleFreePiConsent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		handlerutil.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider": providers.FreePiProviderID,
		"version":  providers.FreePiConsentVersion,
		"text":     FreePiConsentText,
	})
}
