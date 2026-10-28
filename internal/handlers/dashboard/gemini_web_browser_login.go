package dashboard

import (
	"context"
	"errors"
	"net/http"
	"time"

	"9router/proxy/internal/browserlogin"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// The browser sign-in for Gemini Web.
//
// The credential this provider needs is a Google session, and a session
// copied out of a browser that is still running is a session two parties
// rotate: the browser keeps issuing a fresh timestamp cookie, and the router
// keeps issuing one of its own, and they cancel each other out — which signs
// the account out with nothing to show for it.
//
// The flow here opens a browser of its own, with a throwaway profile, and
// reads the session out of it. The profile is deleted and the browser is
// closed on the way out, so nothing is left running that could rotate the
// session afterwards.
//
// Reference: azrinaldi/gemini-stealth-bridge internal/cookies/isolated_login.go
// and its admin endpoint — the flow is that project's shape.

// browserLoginTimeout bounds the whole flow. A sign-in that takes longer than
// this is a sign-in that is not going to happen.
const browserLoginTimeout = 4 * time.Minute

// browserLoginRun is the flow that opens the browser. It is a variable so a
// test can stand in for it, the way the other outbound calls in this package
// are.
var browserLoginRun = browserlogin.Run

// HandleGeminiWebBrowserLogin opens a throwaway browser, waits for a Google
// sign-in, and answers with the session it captured.
//
// The answer is the credential the caller pastes into the connection form.
// It is never stored here: the caller decides which connection it belongs to.
func (h *DashboardHandler) HandleGeminiWebBrowserLogin(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), browserLoginTimeout)
	defer cancel()

	log.Info("gemini-web", "opening a browser for the sign-in")
	result, err := browserLoginRun(ctx)
	if err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, browserlogin.ErrNoBrowser):
			status = http.StatusNotImplemented
		case errors.Is(err, context.DeadlineExceeded):
			status = http.StatusGatewayTimeout
		}
		log.Warn("gemini-web", "the browser sign-in did not finish", "error", err)
		handlerutil.WriteJSONError(w, status, err.Error())
		return
	}

	log.Info("gemini-web", "the browser sign-in finished")
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"cookie": result.CookieHeader,
	})
}
