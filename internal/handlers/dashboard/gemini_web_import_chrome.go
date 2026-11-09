package dashboard

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/browsercookies"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// The import of a session from a browser that is already signed in.
//
// The other way in — a throwaway browser — is the safer of the two, because
// nothing is left running that could rotate the session. This one is the
// convenient one: the browser is already open, already signed in, and the
// session is read out of its own database.
//
// The trade is stated rather than hidden. A browser that is running keeps
// rotating the same session the router is about to renew, and the two cancel
// each other out — which signs the account out. The answer therefore says so
// when it finds a browser that is running, and names the throwaway browser as
// the way to avoid it.
//
// Reference: azrinaldi/gemini-stealth-bridge internal/cookies/chrome_sqlite.go
// and its --import-chrome flag — the reading is that project's shape.

// importChromeTimeout bounds the read of a browser's cookie database.
//
// The read is a file copy and a decryption, not a network call, so it is
// quick. The bound is there so a database on a slow disk cannot hold the
// request open.
const importChromeTimeout = 60 * time.Second

// browserCookiesRead reads a browser's cookie database.
//
// It is a variable so a test can stand in for the read, the way the other
// outbound calls in this package are.
var browserCookiesRead = browsercookies.Read

// browserCookiesReadDomain reads one domain's cookies out of a browser's
// cookie database.
//
// It is a variable so a test can stand in for the read, the way the other
// outbound calls in this package are.
var browserCookiesReadDomain = browsercookies.ReadDomain

// browserCookiesFindDB locates a browser's cookie database.
//
// It is a variable so a test can stand in for the search, the way the other
// outbound calls in this package are.
var browserCookiesFindDB = browsercookies.FindCookieDB

// importChromeRequest is what the caller may ask for.
type importChromeRequest struct {
	// Profile names a browser ("chrome"), a profile ("Profile 4"), or the
	// path of a cookie database. Empty means the first browser on this
	// machine that has one.
	Profile string `json:"profile"`
}

// HandleGeminiWebImportChrome reads a session out of a browser that is
// already signed in.
//
// The answer is the credential the caller pastes into the connection form.
// It is never stored here: the caller decides which connection it belongs to.
func (h *DashboardHandler) HandleGeminiWebImportChrome(w http.ResponseWriter, r *http.Request) {
	request, ok := decodeImportChromeRequest(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), importChromeTimeout)
	defer cancel()

	dbPath, err := browserCookiesFindDB(strings.TrimSpace(request.Profile))
	if err != nil {
		log.Warn("gemini-web", "no browser cookie database was found", "error", err)
		handlerutil.WriteJSONError(w, http.StatusNotFound, err.Error())
		return
	}

	result, err := browserCookiesRead(ctx, dbPath)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, browsercookies.ErrNoKey) {
			status = http.StatusNotImplemented
		}
		log.Warn("gemini-web", "the browser's cookies could not be read", "path", dbPath, "error", err)
		handlerutil.WriteJSONError(w, status, err.Error())
		return
	}

	if result == nil || result.Header == "" {
		handlerutil.WriteJSONError(w, http.StatusNotFound,
			"that browser profile holds no Google session cookies — sign in to gemini.google.com in it first")
		return
	}

	log.Info("gemini-web", "a session was read out of a browser", "path", dbPath)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"cookie":  result.Header,
		"warning": importChromeWarning(dbPath, result),
	})
}

// decodeImportChromeRequest reads the request body.
//
// A body that is not there is not an error: the request asks for the first
// browser on the machine, and that needs nothing.
func decodeImportChromeRequest(w http.ResponseWriter, r *http.Request) (importChromeRequest, bool) {
	var request importChromeRequest
	if r.Body == nil {
		return request, true
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read the request body")
		return request, false
	}
	defer func() { _ = r.Body.Close() }()
	if len(body) == 0 {
		return request, true
	}
	if err := json.Unmarshal(body, &request); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return request, false
	}
	return request, true
}

// importChromeWarning says what the operator should know about the session
// that was just read.
//
// A browser that is running is a browser that is rotating the session the
// router is about to renew, and the two cancel each other out — which signs
// the account out. A profile whose long-lived cookies have expired is a
// profile that was never really signed in.
func importChromeWarning(dbPath string, result *browsercookies.Result) string {
	if len(result.Expired) > 0 {
		return "the long-lived session cookies in that profile are expired (" +
			strings.Join(result.Expired, ", ") + ") — sign in to gemini.google.com in it first"
	}
	if lock := browsercookies.DetectProfileLock(dbPath); lock != "" {
		return "that browser is running with this profile, so it keeps rotating the same session " +
			"this router renews — sign in through a throwaway browser instead, or close the browser first"
	}
	return ""
}
