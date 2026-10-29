// Package browserlogin captures a Google session from an isolated browser.
//
// The problem it solves: Google rotates the session timestamp cookie
// (`__Secure-1PSIDTS`) every few minutes, and the session is bound to that
// cookie's current value. A credential copied out of a browser that is still
// running is therefore a race — the browser keeps rotating the same session
// the router is using, and the two cancel each other out, which signs the
// account out.
//
// The fix is a browser of its own. A throwaway profile signs in once, the
// cookies are read out of it, and the profile is deleted along with the
// browser. Nothing is left running that could rotate the session, so the
// router's own renewal is the only one.
//
// Reference: azrinaldi/gemini-stealth-bridge internal/cookies/isolated_login.go
// — the browser discovery, the disposable profile, the DevTools protocol call
// and the sign-in detection are that project's shape.
package browserlogin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// resolveDisplayEnv returns the environment a browser launch needs, with the
// user session's display filled in when the process environment lacks one.
//
// A gateway started by a service manager (systemd, a supervisor) often has
// no DISPLAY or WAYLAND_DISPLAY even when the machine is a desktop with a
// live session. The session socket is discoverable, and a browser handed it
// can open a real window the operator can sign in to.
func resolveDisplayEnv() []string {
	env := os.Environ()
	if runtime.GOOS != "linux" || os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" {
		return env
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	for _, name := range []string{"wayland-1", "wayland-0"} {
		if runtimeDir != "" {
			if _, err := os.Stat(filepath.Join(runtimeDir, name)); err == nil {
				return append(env, "WAYLAND_DISPLAY="+name)
			}
		}
	}
	for i := 0; i <= 1; i++ {
		if _, err := os.Stat(fmt.Sprintf("/tmp/.X11-unix/X%d", i)); err == nil {
			return append(env, fmt.Sprintf("DISPLAY=:%d", i))
		}
	}
	return env
}

// envNamesDisplay reports whether an environment can open a window: it names
// a display with a non-empty value. This is what the launch gate decides on,
// not os.Getenv — a gateway started by a service manager has neither variable
// set, yet resolveDisplayEnv can still discover the session's socket, and
// refusing the launch before that discovery runs is what made the browser
// refuse to open on a desktop with a live session.
func envNamesDisplay(env []string) bool {
	for _, entry := range env {
		if entry == "DISPLAY=" || entry == "WAYLAND_DISPLAY=" {
			continue
		}
		if strings.HasPrefix(entry, "DISPLAY=") || strings.HasPrefix(entry, "WAYLAND_DISPLAY=") {
			return true
		}
	}
	return false
}

// GeminiAppURL is the page a sign-in has to reach for the session to be
// established.
const GeminiAppURL = "https://gemini.google.com/app"

// pollInterval is how often the browser is asked whether the sign-in has
// completed.
const pollInterval = 1200 * time.Millisecond

// ErrNoBrowser means no browser this flow can drive is installed.
var ErrNoBrowser = errors.New("no Chrome, Chromium, Brave or Edge binary was found on this machine — " +
	"install one, or open an Incognito window, sign in to gemini.google.com, copy the cookie header and paste it here")

// ErrNoSignIn means the browser was open but the sign-in never completed.
var ErrNoSignIn = errors.New("the browser window was closed before the Google sign-in completed")

// Result is what one sign-in produced.
type Result struct {
	// CookieHeader is the session, as a Cookie header.
	CookieHeader string
	// UserAgent is the browser's own user agent string.
	UserAgent string
}

// FindBrowser locates a browser this flow can drive.
//
// The list is the reference bridge's: Chrome, Chromium, Brave and Edge, in
// that order, on the platform's own layout first and the PATH second.
func FindBrowser() string {
	for _, name := range browserNames() {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	for _, path := range platformBrowserPaths() {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

// browserNames are the executables to look for on the PATH.
func browserNames() []string {
	if runtime.GOOS == "windows" {
		return []string{"chrome", "msedge", "brave", "chromium"}
	}
	return []string{
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
		"brave-browser", "microsoft-edge", "microsoft-edge-stable",
	}
}

// platformBrowserPaths are the places the browsers live on each platform.
func platformBrowserPaths() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Vivaldi.app/Contents/MacOS/Vivaldi",
		}
	case "windows":
		return []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`,
		}
	default:
		return []string{
			"/usr/bin/google-chrome",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/usr/bin/brave-browser",
			"/snap/bin/chromium",
			// The system-wide and the per-user Flatpak exports. A Flatpak
			// browser is the common case on a desktop Linux, and it is not
			// on the PATH unless the export is set up by hand.
			"/var/lib/flatpak/exports/bin/com.google.Chrome",
			"/var/lib/flatpak/exports/bin/org.chromium.Chromium",
			"/var/lib/flatpak/exports/bin/com.brave.Browser",
			"/home/" + os.Getenv("USER") + "/.local/share/flatpak/exports/bin/com.google.Chrome",
			"/home/" + os.Getenv("USER") + "/.local/share/flatpak/exports/bin/org.chromium.Chromium",
			"/home/" + os.Getenv("USER") + "/.local/share/flatpak/exports/bin/com.brave.Browser",
		}
	}
}

// Run opens a throwaway browser, waits for a Google sign-in, reads the
// session out of it, and deletes the browser.
//
// The browser is given a disposable profile of its own and a debugging port
// of its own, and both are removed on the way out. Nothing is left running
// that could rotate the session afterwards, which is the whole point: a
// session taken from a browser that keeps running is a session that expires
// and a sign-out that looks unexplained.
func Run(ctx context.Context) (*Result, error) {
	binary := FindBrowser()
	if binary == "" {
		return nil, ErrNoBrowser
	}
	// The gate reads the resolved environment, not the process environment: a
	// gateway started by a service manager has neither display variable set,
	// but the session's socket is discoverable and the browser can still open
	// a window on the operator's desktop.
	displayEnv := resolveDisplayEnv()
	if runtime.GOOS == "linux" && !envNamesDisplay(displayEnv) {
		return nil, errors.New("this machine has no display, so the browser cannot be opened; " +
			"use an Incognito window instead and paste the cookie header")
	}

	profile, err := os.MkdirTemp("", "9router-gemini-login-*")
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
		GeminiAppURL,
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
			return nil, ErrNoSignIn
		case <-ticker.C:
			result, ok := readSession(ctx, port)
			if ok {
				return result, nil
			}
		}
	}
}

// freePort reserves a port for the browser's debugging interface.
func freePort() (int, error) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

// readSession asks the running browser for its cookies and returns the Google
// session among them.
//
// The check for what counts as a session is the site's own: a session is
// established when the timestamp cookie and the session id are both there.
// Anything less is an anonymous page, and an anonymous page is not a
// credential.
func readSession(ctx context.Context, port int) (*Result, bool) {
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	userAgent, webSocketURL := browserEndpoint(callCtx, port, "https://gemini.google.com/")
	if webSocketURL == "" {
		return nil, false
	}

	reply, err := cdpCall(callCtx, webSocketURL, `{"id":1,"method":"Network.getAllCookies"}`)
	if err != nil {
		return nil, false
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
		return nil, false
	}

	seen := map[string]string{}
	parts := make([]string, 0, 16)
	for _, cookie := range payload.Cookies {
		if !strings.HasSuffix(cookie.Domain, "google.com") || cookie.Name == "" || cookie.Value == "" {
			continue
		}
		if _, exists := seen[cookie.Name]; exists {
			continue
		}
		seen[cookie.Name] = cookie.Value
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	if seen["__Secure-1PSID"] == "" || seen["__Secure-1PSIDTS"] == "" {
		return nil, false
	}
	return &Result{CookieHeader: strings.Join(parts, "; "), UserAgent: userAgent}, true
}

// browserEndpoint reads the browser's debugging endpoint.
//
// The browser may have a page open on the site already or not, so the page
// list is consulted first and the browser's own endpoint is the fallback.
//
// sitePrefix narrows the page list to a page on the site the caller is
// signing in to: a page on another site cannot answer for it, and the
// browser endpoint cannot evaluate a script in a page's context at all.
func browserEndpoint(ctx context.Context, port int, sitePrefix string) (userAgent, webSocketURL string) {
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/json/version", nil)
	if err != nil {
		return "", ""
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", ""
	}
	defer func() { _ = response.Body.Close() }()
	var version struct {
		UserAgent            string `json:"User-Agent"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(response.Body).Decode(&version); err != nil {
		return "", ""
	}

	request, err = http.NewRequestWithContext(ctx, http.MethodGet, base+"/json/list", nil)
	if err != nil {
		return version.UserAgent, version.WebSocketDebuggerURL
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		return version.UserAgent, version.WebSocketDebuggerURL
	}
	defer func() { _ = response.Body.Close() }()
	var targets []struct {
		Type                 string `json:"type"`
		URL                  string `json:"url"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(response.Body).Decode(&targets); err != nil {
		return version.UserAgent, version.WebSocketDebuggerURL
	}
	for _, target := range targets {
		if target.Type != "page" || !strings.HasPrefix(target.URL, sitePrefix) {
			continue
		}
		if strings.Contains(target.URL, "accounts.google.com") {
			continue
		}
		if target.WebSocketDebuggerURL != "" {
			return version.UserAgent, target.WebSocketDebuggerURL
		}
	}
	return version.UserAgent, version.WebSocketDebuggerURL
}

// cdpCall sends one DevTools protocol command and reads its answer.
func cdpCall(ctx context.Context, webSocketURL, payload string) ([]byte, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	conn, response, err := dialer.DialContext(ctx, webSocketURL, nil)
	if err != nil {
		return nil, err
	}
	if response != nil && response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(2 * time.Second)
	_ = conn.SetReadDeadline(deadline)

	if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		return nil, err
	}
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return nil, err
		}
		var reply struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(message, &reply); err != nil {
			continue
		}
		if len(reply.Error) > 0 && string(reply.Error) != "null" {
			return nil, fmt.Errorf("the browser refused the request: %s", reply.Error)
		}
		if reply.Result != nil {
			return reply.Result, nil
		}
	}
}
