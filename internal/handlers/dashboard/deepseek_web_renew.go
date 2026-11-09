package dashboard

import (
	"context"
	"errors"
	"net/http"
	"time"

	"9router/proxy/internal/browserlogin"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/proxy/executor"
)

// The renewal of a DeepSeek Web credential.
//
// The credential cannot be renewed by a client — that is measured, not
// assumed: the site's API authenticates with a `userToken` that only a real
// sign-in issues, and the WAF's token is renewed by the browser running the
// WAF's JavaScript challenge. A plain request that presents a valid cookie
// is answered with HTTP 200 and no `Set-Cookie` at all.
//
// So the renewal opens a browser, waits for the sign-in, and stores what the
// browser produced back on the connection row. The operator is in the loop
// because a sign-in needs a human, and the answer says so when it opens.
//
// Unlike the browser sign-in — which answers with a credential for the
// operator to paste — this one writes the credential to the row it was
// asked about. That is the difference: the sign-in is a way in, and the
// renewal is a way to keep a connection working.

// deepSeekRenewalTimeout bounds the whole renewal.
//
// The executor's own bound is the same, and this one is a little longer so
// the executor's answer is what the operator sees when the sign-in times
// out.
const deepSeekRenewalTimeout = 5 * time.Minute

// HandleDeepSeekWebRenew renews one DeepSeek Web connection's credential.
//
// The credential is written to the row the request names, and the answer
// says what was written.
func (h *DashboardHandler) HandleDeepSeekWebRenew(w http.ResponseWriter, r *http.Request) {
	connectionID := getURLParam(r, "id")
	if connectionID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "no connection was named")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), deepSeekRenewalTimeout)
	defer cancel()

	log.Info("deepseek-web", "renewing a credential", "conn", connectionID)
	outcome, err := executor.RenewDeepSeekCredential(ctx, h.Repo, connectionID)
	if err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, browserlogin.ErrNoBrowser):
			status = http.StatusNotImplemented
		case errors.Is(err, context.DeadlineExceeded):
			status = http.StatusGatewayTimeout
		case errors.Is(err, executor.ErrDeepSeekRenewalUnavailable):
			status = http.StatusNotImplemented
		}
		log.Warn("deepseek-web", "the renewal did not finish", "conn", connectionID, "error", err)
		handlerutil.WriteJSONError(w, status, err.Error())
		return
	}

	log.Info("deepseek-web", "the renewal finished", "conn", connectionID)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"renewed":   outcome.Renewed,
		"userToken": outcome.UserToken,
		"cookie":    outcome.Cookie,
	})
}
