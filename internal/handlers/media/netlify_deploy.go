package media

// Netlify relay deploy endpoint (upstream
// src/app/api/proxy-pools/netlify-deploy/route.js).
//
// Netlify's flow differs from the other three runtimes: there is no build step
// and no framework detection. The gateway announces the SHA digests of the two
// artefacts (index.html and the relay function zip), Netlify answers with the
// subset it does not already hold, and the gateway uploads only that subset —
// see netlify_relay.go for the digest contract.

import (
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// POST /api/proxy-pools/netlify-deploy
func (h *MediaHandler) HandleNetlifyDeploy(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	var reqBody struct {
		NetlifyToken string `json:"netlifyToken"`
		SiteName     string `json:"siteName"`
		ProjectName  string `json:"projectName"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	token := strings.TrimSpace(reqBody.NetlifyToken)
	if token == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Netlify API token is required")
		return
	}

	rawName := reqBody.ProjectName
	if strings.TrimSpace(rawName) == "" {
		rawName = reqBody.SiteName
	}
	siteName := sanitizeNetlifySiteName(rawName)

	deployURL, err := netlifyDeployRelay(r.Context(), h.Client, token, siteName)
	if err != nil {
		handlerutil.WriteJSONError(w, netlifyErrorStatus(err), netlifyErrorMessage(err))
		return
	}

	pool, err := h.Repo.InsertProxyPool(db.ProxyPoolData{
		Name: siteName, ProxyURL: deployURL, NoProxy: "", Type: "netlify", StrictProxy: false,
	})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusCreated, map[string]any{"proxyPool": pool, "deployUrl": deployURL})
}

// netlifyErrorStatus maps a failure from the deploy flow onto the status the
// dashboard renders. A transport fault or a failed poll is a gateway fault;
// anything the API itself rejected keeps the status Netlify gave, because that
// is what the user has to act on (a taken site name is a 409, not a 500).
func netlifyErrorStatus(err error) int {
	var apiErr *netlifyError
	if errors.As(err, &apiErr) {
		return apiErr.status
	}
	return http.StatusInternalServerError
}

func netlifyErrorMessage(err error) string {
	var apiErr *netlifyError
	if errors.As(err, &apiErr) {
		return apiErr.message
	}
	return err.Error()
}