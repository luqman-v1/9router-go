package dashboard

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/browsercookies"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// The import of a DeepSeek Web credential from a browser that is already
// signed in.
//
// This is the convenient way in, and it is the one that matches how the
// credential is actually held: the site keeps the API's `userToken` in
// localStorage and the WAF's cookie in the cookie jar, and a browser the
// operator is already signed in to has both.
//
// The trade against the throwaway browser is stated rather than hidden. A
// browser that is running keeps the same session the router is about to
// renew, and the two cancel each other out. The answer therefore says so
// when it finds a browser that is running, and names the throwaway browser as
// the way to avoid it.
//
// The credential is read, never written: the caller decides which connection
// it belongs to.

// deepSeekImportTimeout bounds the read of a browser profile.
//
// The read is a file copy and a decryption, not a network call, so it is
// quick. The bound is there so a profile on a slow disk cannot hold the
// request open.
const deepSeekImportTimeout = 60 * time.Second

// deepSeekLocalStorageRead reads one value out of a profile's localStorage.
//
// It is a variable so a test can stand in for the read, the way the other
// outbound calls in this package are.
var deepSeekLocalStorageRead = browsercookies.ReadLocalStorage

// deepSeekCookieDomain is the domain the credential's cookies belong to.
const deepSeekCookieDomain = "deepseek"

// deepSeekLocalStorageOrigin is the origin the token is stored under.
const deepSeekLocalStorageOrigin = "https://chat.deepseek.com"

// deepSeekLocalStorageKey is the name the site stores the token under.
const deepSeekLocalStorageKey = "userToken"

// HandleDeepSeekWebImportChrome reads the credential out of a browser that
// is already signed in to chat.deepseek.com.
//
// The answer is the credential the caller pastes into the connection form.
func (h *DashboardHandler) HandleDeepSeekWebImportChrome(w http.ResponseWriter, r *http.Request) {
	request, ok := decodeImportChromeRequest(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), deepSeekImportTimeout)
	defer cancel()

	dbPath, err := browserCookiesFindDB(strings.TrimSpace(request.Profile))
	if err != nil {
		log.Warn("deepseek-web", "no browser cookie database was found", "error", err)
		handlerutil.WriteJSONError(w, http.StatusNotFound, err.Error())
		return
	}

	cookies, err := browserCookiesReadDomain(ctx, dbPath, deepSeekCookieDomain)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, browsercookies.ErrNoKey) {
			status = http.StatusNotImplemented
		}
		log.Warn("deepseek-web", "the browser's cookies could not be read", "path", dbPath, "error", err)
		handlerutil.WriteJSONError(w, status, err.Error())
		return
	}

	// The token is the credential the API authenticates with, and it lives in
	// the profile's localStorage rather than in its cookie jar. A profile that
	// holds the cookies but not the token is a profile that is not signed in.
	profile := browsercookies.ProfileFromCookieDB(dbPath)
	token, err := deepSeekLocalStorageRead(profile, deepSeekLocalStorageOrigin, deepSeekLocalStorageKey)
	if err != nil {
		log.Warn("deepseek-web", "the profile holds no DeepSeek token", "path", profile, "error", err)
		handlerutil.WriteJSONError(w, http.StatusNotFound,
			"that browser profile holds no DeepSeek userToken — sign in to chat.deepseek.com in it first")
		return
	}

	log.Info("deepseek-web", "a credential was read out of a browser", "path", dbPath)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"userToken": token,
		"cookie":    cookies.Header,
		"warning":   importChromeWarning(dbPath, cookies),
	})
}
