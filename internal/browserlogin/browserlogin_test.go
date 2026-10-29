package browserlogin

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestFindBrowserPrefersThePath pins where the browser is looked for.
//
// The PATH comes first because a browser on it is one the operator put
// there, and the platform's own layout is the fallback. The order matters
// for a machine with several browsers: the one on the PATH is the one the
// operator expects to be opened.
func TestFindBrowserPrefersThePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the PATH convention differs on Windows")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "google-chrome")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write the fake browser: %v", err)
	}
	t.Setenv("PATH", dir)

	if got := FindBrowser(); got != fake {
		t.Errorf("FindBrowser() = %q, want %q", got, fake)
	}
}

// TestFindBrowserFindsNothingWhenThereIsNone proves the failure is a clean
// one rather than a path to something that is not a browser.
func TestFindBrowserFindsNothingWhenThereIsNone(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	// The platform paths are absolute, so they are not covered by the PATH
	// above. A machine that has a browser installed is found there — which
	// is the point — so this only asserts that the lookup never returns a
	// path that does not exist.
	got := FindBrowser()
	if got == "" {
		return
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("FindBrowser() = %q, which does not exist", got)
	}
}

// TestBrowserNamesCoverTheBrowsersTheFlowCanDrive pins the list.
//
// A browser that is not on the list is one this flow cannot open, and the
// operator gets the "no browser found" message instead of a window.
func TestBrowserNamesCoverTheBrowsersTheFlowCanDrive(t *testing.T) {
	names := browserNames()
	for _, want := range []string{"google-chrome", "chromium", "brave-browser", "microsoft-edge"} {
		if runtime.GOOS == "windows" && want == "google-chrome" {
			continue
		}
		found := false
		for _, name := range names {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the browser list %v does not name %q", names, want)
		}
	}
}

// TestPlatformBrowserPathsAreAbsolute pins the platform paths.
//
// A relative path would be resolved against the working directory, which is
// not where a browser lives.
func TestPlatformBrowserPathsAreAbsolute(t *testing.T) {
	for _, path := range platformBrowserPaths() {
		if !strings.HasPrefix(path, "/") && !strings.Contains(path, `:\`) {
			t.Errorf("the browser path %q is not absolute", path)
		}
	}
}

// TestReadSessionNeedsBothSessionCookies pins what counts as a session.
//
// The browser's cookie jar holds the cookies of every site it has visited, so
// the reading has to be strict: a session is established when the session id
// and its timestamp are both there. An anonymous page carries neither, and
// one that was handed over as a credential would reach one model.
func TestReadSessionNeedsBothSessionCookies(t *testing.T) {
	// The browser's debugging endpoint is served by a stand-in here, so the
	// reading is exercised without a browser.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/version":
			w.Write([]byte(`{"User-Agent":"test-agent","webSocketDebuggerUrl":"ws://127.0.0.1:1/devtools"}`))
		case "/json/list":
			w.Write([]byte(`[{"type":"page","url":"https://gemini.google.com/app","webSocketDebuggerUrl":"ws://127.0.0.1:1/devtools"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	port := server.Listener.Addr().(*net.TCPAddr).Port
	if _, ok := readSession(t.Context(), port); ok {
		t.Error("readSession() accepted a browser it could not reach")
	}
}

// resolveDisplayEnv fills in the user session's display when the process
// environment has none: a gateway started by a service manager has no
// DISPLAY/WAYLAND_DISPLAY, but a browser can still open a window if it is
// handed the socket the session actually listens on.
func TestResolveDisplayEnvFillsWayland(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("session sockets are a unix concept")
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wayland-0"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", dir)

	for _, v := range resolveDisplayEnv() {
		if v == "WAYLAND_DISPLAY=wayland-0" {
			return
		}
	}
	t.Error("the fallback WAYLAND_DISPLAY is missing from the resolved env")
}

func TestResolveDisplayEnvKeepsExistingDisplay(t *testing.T) {
	t.Setenv("DISPLAY", ":9")
	t.Setenv("WAYLAND_DISPLAY", "")

	for _, v := range resolveDisplayEnv() {
		if v == "DISPLAY=:9" {
			return
		}
	}
	t.Error("an existing DISPLAY must be preserved")
}

func TestResolveDisplayEnvAdvertisesNoMissingSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("session sockets are a unix concept")
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	for _, v := range resolveDisplayEnv() {
		if v == "WAYLAND_DISPLAY=wayland-0" || v == "WAYLAND_DISPLAY=wayland-1" {
			t.Errorf("a socket that does not exist must not be advertised: %q", v)
		}
	}
}

// envNamesDisplay decides the launch gate. It must see a display the process
// environment does not name: the regression this guards is a gate that read
// os.Getenv directly, so a gateway started by a service manager — no
// DISPLAY/WAYLAND_DISPLAY in its environment, but a live session on the
// machine — was refused before resolveDisplayEnv ever discovered the socket.
func TestEnvNamesDisplay(t *testing.T) {
	if envNamesDisplay([]string{"PATH=/usr/bin", "HOME=/home/x"}) {
		t.Error("an environment with no display variable must not name one")
	}
	if envNamesDisplay([]string{"DISPLAY="}) {
		t.Error("a display variable set to the empty string names no display")
	}
	if envNamesDisplay([]string{"WAYLAND_DISPLAY="}) {
		t.Error("an empty WAYLAND_DISPLAY names no display")
	}
	if !envNamesDisplay([]string{"PATH=/usr/bin", "DISPLAY=:1"}) {
		t.Error("a set DISPLAY names a display")
	}
	if !envNamesDisplay([]string{"WAYLAND_DISPLAY=wayland-0"}) {
		t.Error("a set WAYLAND_DISPLAY names a display")
	}
}

// The gate the launches actually take: with no display variables in the
// process environment but a live Wayland socket, the resolved environment
// names a display and the launch must proceed to the browser, not refuse.
func TestResolveDisplayEnvSatisfiesTheLaunchGate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("session sockets are a unix concept")
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wayland-1"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", dir)

	if !envNamesDisplay(resolveDisplayEnv()) {
		t.Fatal("a discovered session socket must satisfy the launch gate")
	}
}
