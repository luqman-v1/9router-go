package browsercookies

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNormalizeCookieHeaderInput pins the Muse Web JSON-cookie normalization
// contract ported from decolua/9router @0760c302
// (open-sse/utils/webCookieAuth.js normalizeCookieHeaderInput).
//
// A Muse Web credential is the site's Cookie header (it carries hatch_sess).
// Operators paste it as a plain header or as a Cookie-Editor JSON export (a
// bare array of {name, value} objects or a {cookies: [...]} object). The
// semicolon-delimited cookie parser only understood the header, so an exported
// jar was rejected as a missing hatch_sess cookie. Normalization turns every
// supported spelling into a Cookie header, preserves ordinary header strings
// and '=' inside values, and rejects malformed or unsupported JSON before any
// request is made.
func TestNormalizeCookieHeaderInput(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{
			name: "JSON array of cookie objects",
			raw:  `[{"name":"hatch_sess","value":"abc==","domain":".muse.ai","path":"/"},{"name":"hatch_gw","value":"gw1"}]`,
			want: "hatch_sess=abc==; hatch_gw=gw1",
		},
		{
			name: "cookies object export",
			raw:  `{"cookies":[{"name":"hatch_sess","value":"abc=="},{"name":"hatch_gw","value":"gw1"}]}`,
			want: "hatch_sess=abc==; hatch_gw=gw1",
		},
		{
			name: "ordinary header preserved",
			raw:  "hatch_sess=abc==; hatch_gw=gw1",
			want: "hatch_sess=abc==; hatch_gw=gw1",
		},
		{
			name: "bearer prefix stripped",
			raw:  "Bearer hatch_sess=abc==",
			want: "hatch_sess=abc==",
		},
		{
			name: "cookie label stripped",
			raw:  "Cookie: hatch_sess=abc==",
			want: "hatch_sess=abc==",
		},
		{
			name: "empty array normalizes to empty header",
			raw:  "[]",
			want: "",
		},
		{
			name: "empty cookies object normalizes to empty header",
			raw:  `{"cookies":[]}`,
			want: "",
		},
		{
			name: "empty input stays empty",
			raw:  "",
			want: "",
		},
		{
			name:    "malformed JSON rejected",
			raw:     `[{"name":"hatch_sess","value":`,
			wantErr: true,
		},
		{
			name:    "cookies field not an array rejected",
			raw:     `{"cookies":"not an array"}`,
			wantErr: true,
		},
		{
			name:    "cookie entry missing value rejected",
			raw:     `{"cookies":[{"name":"hatch_sess"}]}`,
			wantErr: true,
		},
		{
			name:    "cookie entry missing name rejected",
			raw:     `[{"value":"abc"}]`,
			wantErr: true,
		},
		{
			name:    "non-object entry rejected",
			raw:     `["hatch_sess=abc"]`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeCookieHeaderInput(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizeCookieHeaderInput(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("NormalizeCookieHeaderInput(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestBuildHeaderUsesTheCanonicalOrder pins the order of the header.
//
// The order is fixed by the reference bridge
// (azrinaldi/gemini-stealth-bridge internal/cookies/jar.go, EssentialNames).
// It does not change what the site accepts, and it does change what two
// reads look like side by side, which is what an operator compares when
// asking whether the session was renewed.
func TestBuildHeaderUsesTheCanonicalOrder(t *testing.T) {
	cookies := []Cookie{
		{Host: ".google.com", Name: "NID", Value: "nid-value"},
		{Host: ".google.com", Name: "__Secure-1PSIDTS", Value: "ts-value"},
		{Host: ".google.com", Name: "__Secure-1PSID", Value: "psid-value"},
		{Host: ".google.com", Name: "SID", Value: "sid-value"},
		// A cookie outside the session set is not part of the header.
		{Host: ".google.com", Name: "COMPASS", Value: "compass-value"},
	}

	got := buildGoogleHeader(cookies)
	want := "SID=sid-value; __Secure-1PSID=psid-value; __Secure-1PSIDTS=ts-value; NID=nid-value"
	if got != want {
		t.Errorf("buildHeader() = %q, want %q", got, want)
	}
}

// TestBuildHeaderPrefersTheCookieThatCoversEveryHost proves which of two
// cookies of the same name is used.
//
// A cookie held for `.google.com` is sent to every Google host; one held for
// `accounts.google.com` is not. The one that covers every host is the one
// that works everywhere, so it is the one that goes in.
func TestBuildHeaderPrefersTheCookieThatCoversEveryHost(t *testing.T) {
	cookies := []Cookie{
		{Host: ".google.com", Name: "SID", Value: "the-wide-one"},
		{Host: "accounts.google.com", Name: "SID", Value: "the-narrow-one"},
	}
	got := buildGoogleHeader(cookies)
	if !strings.Contains(got, "SID=the-wide-one") {
		t.Errorf("buildHeader() = %q, want the cookie that covers every host", got)
	}
}

// TestBuildHeaderSkipsAnEmptyValue proves a nameless value is left out.
//
// An empty cookie is not a cookie, and sending the name without a value
// would be sending something the site has to make sense of.
func TestBuildHeaderSkipsAnEmptyValue(t *testing.T) {
	got := buildGoogleHeader([]Cookie{
		{Host: ".google.com", Name: "SID", Value: ""},
		{Host: ".google.com", Name: "NID", Value: "nid-value"},
	})
	if strings.Contains(got, "SID=") {
		t.Errorf("buildHeader() = %q, want no empty cookie", got)
	}
	if got != "NID=nid-value" {
		t.Errorf("buildHeader() = %q, want %q", got, "NID=nid-value")
	}
}

// TestFindCookieDBFindsAProfilePath proves a path is looked at.
//
// An operator who knows which database holds the session should be able to
// name it, rather than hoping the guess about which browser is on the
// machine is the right one.
func TestFindCookieDBFindsAProfilePath(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "Default", "Network")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatalf("make the profile: %v", err)
	}
	dbPath := filepath.Join(profile, "Cookies")
	if err := os.WriteFile(dbPath, []byte("sqlite"), 0o600); err != nil {
		t.Fatalf("write the database: %v", err)
	}

	got, err := FindCookieDB(dbPath)
	if err != nil {
		t.Fatalf("FindCookieDB() error = %v", err)
	}
	if got != dbPath {
		t.Errorf("FindCookieDB() = %q, want %q", got, dbPath)
	}
}

// TestFindCookieDBReportsAMissingPath proves a missing database is an error
// and not a silent empty answer.
//
// An empty answer would look like a browser with no session, which is a
// different thing and a different fix.
func TestFindCookieDBReportsAMissingPath(t *testing.T) {
	_, err := FindCookieDB(filepath.Join(t.TempDir(), "nope", "Cookies"))
	if err == nil {
		t.Fatal("FindCookieDB() accepted a path that does not exist")
	}
}

// TestDetectProfileLockFindsTheLock proves a running browser is noticed.
//
// The lock is the browser saying it is running with that profile, and that
// is what makes the imported session expire and then sign the account out.
func TestDetectProfileLockFindsTheLock(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "Default")
	if err := os.MkdirAll(filepath.Join(profile, "Network"), 0o755); err != nil {
		t.Fatalf("make the profile: %v", err)
	}
	lock := filepath.Join(profile, "SingletonLock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatalf("write the lock: %v", err)
	}

	got := DetectProfileLock(filepath.Join(profile, "Network", "Cookies"))
	if got != lock {
		t.Errorf("DetectProfileLock() = %q, want %q", got, lock)
	}
}

// TestDetectProfileLockReportsNoLock proves a closed browser is not
// reported as running.
func TestDetectProfileLockReportsNoLock(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "Default")
	if err := os.MkdirAll(filepath.Join(profile, "Network"), 0o755); err != nil {
		t.Fatalf("make the profile: %v", err)
	}
	if got := DetectProfileLock(filepath.Join(profile, "Network", "Cookies")); got != "" {
		t.Errorf("DetectProfileLock() = %q, want nothing", got)
	}
}

// TestExpiringSessionCookiesIgnoresTheRotatingOne pins what counts as a dead
// session.
//
// `__Secure-1PSIDTS` is rotated every few minutes, so treating a rotation
// as a sign of a dead session would cry wolf on every single read.
func TestExpiringSessionCookiesIgnoresTheRotatingOne(t *testing.T) {
	result := &Result{Cookies: []Cookie{
		{Name: "__Secure-1PSIDTS", ExpiresAt: 1},
		{Name: "SID", ExpiresAt: 1},
	}}
	got := ExpiringSessionCookies(result, 0)
	if len(got) != 1 || got[0] != "SID" {
		t.Errorf("ExpiringSessionCookies() = %v, want [SID]", got)
	}
}

// TestBrowserFromDBPathNamesTheBrowser proves the path is read for what it
// is.
//
// It is what picks the one Keychain service to ask on macOS, so that the
// user approves one item rather than one per browser on the machine.
func TestBrowserFromDBPathNamesTheBrowser(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/home/u/.config/google-chrome/Default/Network/Cookies", want: "chrome"},
		{path: "/home/u/.config/BraveSoftware/Brave-Browser/Default/Cookies", want: "brave"},
		{path: `/home/u/AppData/Local/Microsoft/Edge/User Data/Default/Cookies`, want: "edge"},
		{path: "/home/u/.config/chromium/Default/Cookies", want: "chromium"},
		{path: "/home/u/.config/vivaldi/Default/Cookies", want: "vivaldi"},
	}
	for _, tt := range tests {
		if got := browserFromDBPath(tt.path); got != tt.want {
			t.Errorf("browserFromDBPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
