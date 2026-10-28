// Package browserlogin captures a DeepSeek web session from an isolated
// browser.
//
// The problem it solves is the same one the Google flow solves, with a
// difference: DeepSeek's credential is not a cookie.
//
// The site's API authenticates with a Bearer token, and the token is not
// something the site will reissue on demand: it is minted by a real sign-in
// and lives in localStorage under `userToken`. The WAF cookie
// (`aws-waf-token`) is a second, separate thing — it is what the WAF reads
// on the document path, and it is renewed only by a browser that runs the
// WAF's own JavaScript challenge.
//
// So a credential for this provider is TWO values, and a refresh has to
// produce both:
//
//   - the userToken, which the API authenticates with, and
//   - the site's own cookies, which the WAF reads and which the router
//     replays.
//
// A browser sign-in produces both in one step, which is what makes the
// refresh a button instead of a DevTools ritual.
package browserlogin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DeepSeekAppURL is the page a sign-in has to reach for the session to be
// established.
const DeepSeekAppURL = "https://chat.deepseek.com/"

// deepSeekHost is the domain the credential's cookies belong to.
const deepSeekHost = "deepseek.com"

// DeepSeekResult is what one DeepSeek sign-in produced.
type DeepSeekResult struct {
	// UserToken is the API credential: the value the site keeps in
	// localStorage under `userToken`.
	UserToken string

	// CookieHeader is the site's own session cookies, as a Cookie header.
	//
	// This includes the WAF's token, which is what the site reads to decide
	// whether a request comes from a browser.
	CookieHeader string

	// UserAgent is the browser's own user agent string.
	UserAgent string
}

// ErrNoDeepSeekSignIn means the browser was open but the sign-in never
// completed.
var ErrNoDeepSeekSignIn = errors.New("the browser window was closed before the DeepSeek sign-in completed")

// browserProfileRoot is where a throwaway profile is created.
//
// The default is the system's temporary directory, which every browser can
// reach. A Flatpak browser cannot: the sandbox gives it a private /tmp, so a
// profile the process created on the host is invisible to it, and the
// browser silently falls back to its own default profile — the one the
// operator's daily browsing uses, which is the opposite of throwaway. A
// Flatpak browser does see the home directory, so the profile goes there.
func browserProfileRoot(binary string) string {
	if strings.Contains(binary, string(os.PathSeparator)+"flatpak"+string(os.PathSeparator)+"exports"+string(os.PathSeparator)) {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return os.TempDir()
		}
		return filepath.Join(home, ".cache")
	}
	return os.TempDir()
}

// RunDeepSeek opens a throwaway browser, waits for a DeepSeek sign-in, reads
// the credential out of it, and deletes the browser.
//
// The browser is given a disposable profile of its own and a debugging port
// of its own, and both are removed on the way out. Nothing is left running
// that could rotate the session afterwards: a session taken from a browser
// that keeps running is a session that expires and a sign-out that looks
// unexplained.
func RunDeepSeek(ctx context.Context) (*DeepSeekResult, error) {
	binary := FindBrowser()
	if binary == "" {
		return nil, ErrNoBrowser
	}
	// Same gate as the Gemini launch: the resolved environment decides, so a
	// service-manager gateway on a desktop with a live session is not refused
	// merely for lacking the display variables.
	displayEnv := resolveDisplayEnv()
	if runtime.GOOS == "linux" && !envNamesDisplay(displayEnv) {
		return nil, errors.New("this machine has no display, so the browser cannot be opened; " +
			"use an Incognito window instead and paste the userToken from chat.deepseek.com localStorage")
	}

	profile, err := os.MkdirTemp(browserProfileRoot(binary), "9router-deepseek-login-*")
	if err != nil {
		return nil, fmt.Errorf("create the temporary browser profile: %w", err)
	}
	// The profile holds a signed-in session. Leaving it on disk would leave
	// the credential in a temporary directory nothing cleans up.
	defer func() { _ = os.RemoveAll(profile) }()

	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("allocate a debugging port: %w", err)
	}

	command := exec.CommandContext(ctx, binary,
		fmt.Sprintf("--user-data-dir=%s", profile),
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--remote-allow-origins=*",
		"--no-first-run",
		"--no-default-browser-check",
		"--new-window",
		DeepSeekAppURL,
	)
	command.Env = displayEnv
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("open the browser: %w", err)
	}
	// The browser is killed on the way out whatever happens. A browser left
	// running would keep rotating the session it just handed over.
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
		}
	}()

	exited := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(exited)
	}()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-exited:
			return nil, ErrNoDeepSeekSignIn
		case <-ticker.C:
			result, ok := readDeepSeekSession(ctx, port)
			if ok {
				return result, nil
			}
		}
	}
}

// readDeepSeekSession asks the running browser for its cookies and its
// localStorage, and returns the credential if a sign-in has completed.
//
// The check for what counts as a sign-in is the site's own: the userToken
// exists in localStorage only once the account is signed in. A page that is
// still anonymous has no token, and an anonymous page is not a credential.
//
// The cookies are read at the same moment, because the two have to come
// from the same browser: the WAF token in the cookie jar is tied to the
// session that produced it, and a token from one browser with a cookie from
// another is worse than either alone.
func readDeepSeekSession(ctx context.Context, port int) (*DeepSeekResult, bool) {
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	userAgent, webSocketURL := browserEndpoint(callCtx, port, DeepSeekAppURL)
	if webSocketURL == "" {
		return nil, false
	}

	cookies, ok := readDeepSeekCookies(callCtx, webSocketURL)
	if !ok {
		return nil, false
	}
	token, ok := readDeepSeekUserToken(callCtx, webSocketURL)
	if !ok {
		return nil, false
	}
	return &DeepSeekResult{
		UserToken:    token,
		CookieHeader: cookies,
		UserAgent:    userAgent,
	}, true
}

// readDeepSeekCookies reads the site's own cookies out of the browser.
func readDeepSeekCookies(ctx context.Context, webSocketURL string) (string, bool) {
	reply, err := cdpCall(ctx, webSocketURL, `{"id":1,"method":"Network.getAllCookies"}`)
	if err != nil {
		return "", false
	}
	// cdpCall already unwraps the protocol's own `result` envelope, so the
	// cookie list is at the top level of what comes back.
	var payload struct {
		Cookies []struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Domain string `json:"domain"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(reply, &payload); err != nil {
		return "", false
	}

	seen := map[string]string{}
	parts := make([]string, 0, 8)
	for _, cookie := range payload.Cookies {
		if !strings.HasSuffix(cookie.Domain, deepSeekHost) || cookie.Name == "" || cookie.Value == "" {
			continue
		}
		if _, exists := seen[cookie.Name]; exists {
			continue
		}
		seen[cookie.Name] = cookie.Value
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "; "), true
}

// readDeepSeekUserToken reads the API credential out of the page's
// localStorage.
//
// The value is read from the page the browser is on, so the page has to be
// on the site — a sign-in redirects there, and the read is retried until it
// answers.
//
// The site stores the token as a JSON OBJECT, not as a bare string: the
// value in localStorage is `{"value":"…","__version":"0"}`. A page that is
// not signed in holds `{"value":null,"__version":"0"}` — the object is
// there, the credential in it is not. Both shapes are read, because the
// bare-string shape is what a value copied out of DevTools looks like.
func readDeepSeekUserToken(ctx context.Context, webSocketURL string) (string, bool) {
	// The expression is JS SOURCE, not a JSON value: Runtime.evaluate
	// evaluates what it is given as JavaScript. Marshalling it first would
	// send Chrome a JSON string literal, which it evaluates to the source
	// text itself rather than to the token. The payload below is marshalled
	// as a whole, so a value that needs escaping still cannot break the
	// request.
	expression := `localStorage.getItem("userToken")`
	payload, err := json.Marshal(map[string]any{
		"id":     2,
		"method": "Runtime.evaluate",
		"params": map[string]any{"expression": expression, "returnByValue": true},
	})
	if err != nil {
		return "", false
	}
	reply, err := cdpCall(ctx, webSocketURL, string(payload))
	if err != nil {
		return "", false
	}
	// cdpCall already unwraps the protocol's own `result` envelope, so what
	// comes back is the evaluation's own result: `{"type":…,"value":…}`.
	var evaluated struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(reply, &evaluated); err != nil {
		return "", false
	}
	if len(evaluated.Value) == 0 {
		return "", false
	}
	token := DeepSeekUserTokenFromLocalStorage(string(evaluated.Value))
	if token == "" {
		// The page is on the site but holds no credential: it is an
		// anonymous page, and an anonymous page is not a sign-in.
		return "", false
	}
	return token, true
}

// DeepSeekUserTokenFromLocalStorage normalizes a stored credential.
//
// The site stores the token as a JSON object — `{"value":"…","__version":"0"}`
// — and a value that carries a null in it is a page that is not signed in.
// A value copied out of DevTools is sometimes copied as the JSON-encoded
// string instead. Every spelling is accepted here, because the difference
// is a detail of how the value was read and not a difference of credential.
func DeepSeekUserTokenFromLocalStorage(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "{") {
		var stored struct {
			Value *string `json:"value"`
		}
		if err := json.Unmarshal([]byte(trimmed), &stored); err != nil {
			return ""
		}
		// A stored null is the site saying "no credential here", which is
		// what an unsigned-in page holds.
		if stored.Value == nil {
			return ""
		}
		return strings.TrimSpace(*stored.Value)
	}
	if !strings.HasPrefix(trimmed, "\"") {
		return trimmed
	}
	var value string
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return trimmed
	}
	return strings.TrimSpace(value)
}
