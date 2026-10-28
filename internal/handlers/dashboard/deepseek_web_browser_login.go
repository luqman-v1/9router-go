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

// The browser sign-in for DeepSeek Web.
//
// The credential this provider needs is a browser session, and it comes in
// two parts that only a real browser can produce together:
//
//   - the userToken, which is the API's Bearer credential. The site mints it
//     during a sign-in and keeps it in localStorage; nothing else issues
//     one, so there is no "refresh token" to exchange.
//   - the site's own cookies, which the WAF reads. The WAF's token is
//     renewed by the browser running the WAF's JavaScript challenge, and
//     that challenge is what makes the cookie worth having.
//
// A browser sign-in produces both in one step, and the profile it uses is
// deleted on the way out, so nothing is left running that could rotate the
// session afterwards — the same reasoning as the Gemini Web flow, which is
// where the shape of this comes from.

// deepSeekBrowserLoginTimeout bounds the whole flow. A sign-in that takes
// longer than this is a sign-in that is not going to happen.
const deepSeekBrowserLoginTimeout = 4 * time.Minute

// deepSeekBrowserLoginRun is the flow that opens the browser. It is a
// variable so a test can stand in for it, the way the other outbound calls
// in this package are.
var deepSeekBrowserLoginRun = browserlogin.RunDeepSeek

// HandleDeepSeekWebBrowserLogin opens a throwaway browser, waits for a
// DeepSeek sign-in, and answers with the credential it captured.
//
// The answer is the credential the caller pastes into the connection form.
// It is never stored here: the caller decides which connection it belongs
// to.
func (h *DashboardHandler) HandleDeepSeekWebBrowserLogin(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), deepSeekBrowserLoginTimeout)
	defer cancel()

	log.Info("deepseek-web", "opening a browser for the sign-in")
	result, err := deepSeekBrowserLoginRun(ctx)
	if err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, browserlogin.ErrNoBrowser):
			status = http.StatusNotImplemented
		case errors.Is(err, context.DeadlineExceeded):
			status = http.StatusGatewayTimeout
		}
		log.Warn("deepseek-web", "the browser sign-in did not finish", "error", err)
		handlerutil.WriteJSONError(w, status, err.Error())
		return
	}

	log.Info("deepseek-web", "the browser sign-in finished")
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"userToken": result.UserToken,
		"cookie":    result.CookieHeader,
	})
}
