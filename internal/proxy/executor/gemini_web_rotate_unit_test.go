package executor

import (
	"testing"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

// TestGeminiWebAbsorbSetCookies pins how a renewal is merged into a
// credential.
//
// The renewed value has to replace the old one, a cookie the site did not
// mention has to survive, and a cookie the site deleted has to go — a
// credential that keeps a deleted cookie is a credential the site reads as
// cancelled.
func TestGeminiWebAbsorbSetCookies(t *testing.T) {
	tests := []struct {
		name       string
		credential string
		issued     []*fhttp.Cookie
		want       string
		wantChange bool
	}{
		{
			name:       "a renewed value replaces the old one",
			credential: "__Secure-1PSID=psid; __Secure-1PSIDTS=old-ts",
			issued:     []*fhttp.Cookie{{Name: "__Secure-1PSIDTS", Value: "new-ts"}},
			want:       "__Secure-1PSID=psid; __Secure-1PSIDTS=new-ts",
			wantChange: true,
		},
		{
			name:       "a cookie the site did not mention survives",
			credential: "__Secure-1PSID=psid; SAPISID=sapisid",
			issued:     []*fhttp.Cookie{{Name: "__Secure-1PSIDTS", Value: "new-ts"}},
			want:       "__Secure-1PSID=psid; SAPISID=sapisid; __Secure-1PSIDTS=new-ts",
			wantChange: true,
		},
		{
			name:       "a deleted cookie is dropped",
			credential: "__Secure-1PSID=psid; __Secure-1PSIDTS=old-ts",
			issued:     []*fhttp.Cookie{{Name: "__Secure-1PSIDTS", Value: "", MaxAge: -1}},
			want:       "__Secure-1PSID=psid",
			wantChange: true,
		},
		{
			name:       "nothing issued changes nothing",
			credential: "__Secure-1PSID=psid; __Secure-1PSIDTS=old-ts",
			issued:     nil,
			want:       "__Secure-1PSID=psid; __Secure-1PSIDTS=old-ts",
			wantChange: false,
		},
		{
			name:       "the same value is not a change",
			credential: "__Secure-1PSID=psid; __Secure-1PSIDTS=old-ts",
			issued:     []*fhttp.Cookie{{Name: "__Secure-1PSIDTS", Value: "old-ts"}},
			want:       "__Secure-1PSID=psid; __Secure-1PSIDTS=old-ts",
			wantChange: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := geminiWebAbsorbSetCookies(tt.credential, tt.issued)
			if got != tt.want {
				t.Errorf("absorb = %q, want %q", got, tt.want)
			}
			if changed != tt.wantChange {
				t.Errorf("changed = %v, want %v", changed, tt.wantChange)
			}
		})
	}
}

// TestGeminiWebClampRotateInterval pins the interval guard.
//
// A gap below a minute earns a 429 from the site, and a gap above an hour is
// longer than the cookie itself lasts, so the session would lapse before the
// next renewal.
func TestGeminiWebClampRotateInterval(t *testing.T) {
	tests := []struct {
		name     string
		interval time.Duration
		want     time.Duration
	}{
		{name: "no interval", interval: 0, want: geminiWebRotateDefaultInterval},
		{name: "below a minute", interval: 5 * time.Second, want: geminiWebRotateMinInterval},
		{name: "a sane interval passes through", interval: 9 * time.Minute, want: 9 * time.Minute},
		{name: "above an hour", interval: 2 * time.Hour, want: geminiWebRotateMaxInterval},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := geminiWebClampRotateInterval(tt.interval); got != tt.want {
				t.Errorf("clamp(%v) = %v, want %v", tt.interval, got, tt.want)
			}
		})
	}
}

// TestGeminiWebParseRotatePage pins the page parser.
//
// The page carries the rotation id the renew request has to quote, and the
// interval the site wants between renewals.
func TestGeminiWebParseRotatePage(t *testing.T) {
	id, interval, ok := geminiWebParseRotatePage(`init('rot-abc-123', 540, 540);`)
	if !ok {
		t.Fatal("parseRotatePage() ok = false, want true")
	}
	if id != "rot-abc-123" {
		t.Errorf("id = %q, want %q", id, "rot-abc-123")
	}
	if interval != 540*time.Second {
		t.Errorf("interval = %v, want %v", interval, 540*time.Second)
	}
	if _, _, ok := geminiWebParseRotatePage("<html>nothing here</html>"); ok {
		t.Error("parseRotatePage() ok = true for a page with no rotation id")
	}
}

// TestGeminiWebRotationSkipsACredentialItCannotRenew pins the selection.
//
// A connection with no credential, or one whose credential carries no session
// timestamp, has nothing to renew: the anonymous path needs no renewal, and
// a credential without the timestamp is one the site answers as a visitor's
// whatever the router does with it.
func TestGeminiWebRotationSkipsACredentialItCannotRenew(t *testing.T) {
	cases := []struct {
		name       string
		credential string
		wantRenew  bool
	}{
		{name: "no credential", credential: "", wantRenew: false},
		{name: "a credential without the session timestamp", credential: "__Secure-1PSID=psid", wantRenew: false},
		{name: "a credential with the session timestamp", credential: "__Secure-1PSID=psid; __Secure-1PSIDTS=ts", wantRenew: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cookie := GeminiWebCookieHeader(tt.credential)
			got := cookie != "" && GeminiWebHasSessionTimestamp(cookie)
			if got != tt.wantRenew {
				t.Errorf("renewable = %v, want %v", got, tt.wantRenew)
			}
		})
	}
}

// TestGeminiWebEvictSession pins the eviction.
//
// A session is keyed by its credential, and a renewed credential is a
// different one, so every renewal would otherwise leave the previous session
// behind — one entry per renewal, forever.
func TestGeminiWebEvictSession(t *testing.T) {
	geminiWebSessionMu.Lock()
	geminiWebSessions = map[string]*geminiWebSession{}
	geminiWebSessionMu.Unlock()

	old := &geminiWebSession{cookie: "old"}
	other := &geminiWebSession{cookie: "other"}
	geminiWebSessionMu.Lock()
	geminiWebSessions["old\x00origin"] = old
	geminiWebSessions["other\x00origin"] = other
	geminiWebSessionMu.Unlock()

	geminiWebEvictSession("old")

	geminiWebSessionMu.Lock()
	_, oldStillThere := geminiWebSessions["old\x00origin"]
	_, otherThere := geminiWebSessions["other\x00origin"]
	geminiWebSessionMu.Unlock()
	if oldStillThere {
		t.Error("the evicted session is still in the map")
	}
	if !otherThere {
		t.Error("evicting one session removed another")
	}
}

// TestGeminiWebRefusedRenewalIsNotARenewal pins the refusal.
//
// The site issues a fresh NID on every answer, including a refusal, so a
// renewal that counted "the site sent something" would report a refused
// renewal as a successful one. The caller would then store a credential that
// reaches one model and believe the session had been renewed — which is the
// outcome this check exists to prevent.
func TestGeminiWebRefusedRenewalIsNotARenewal(t *testing.T) {
	credential := "__Secure-1PSID=psid-value; __Secure-1PSIDTS=old-ts; NID=old-nid"

	// A refusal that carries a fresh NID and nothing else.
	_, changed := geminiWebAbsorbSetCookies(credential, []*fhttp.Cookie{{Name: "NID", Value: "new-nid"}})
	if changed {
		t.Error("a fresh NID was counted as a renewal")
	}

	// A refusal that carries a fresh timestamp is a renewal.
	_, changed = geminiWebAbsorbSetCookies(credential, []*fhttp.Cookie{{Name: "__Secure-1PSIDTS", Value: "new-ts"}})
	if !changed {
		t.Error("a renewed timestamp was not counted as a renewal")
	}

	// A refusal that deletes the timestamp is a change too: the credential
	// the caller would store no longer carries a session.
	_, changed = geminiWebAbsorbSetCookies(credential, []*fhttp.Cookie{{Name: "__Secure-1PSIDTS", Value: "", MaxAge: -1}})
	if !changed {
		t.Error("a deleted timestamp was not counted as a change")
	}
}

// TestGeminiWebRotationOptOut pins the switch.
//
// A credential that belongs to a browser that is still running has to be left
// alone: the browser rotates the same cookie, and the two rotations cancel
// each other out. Only an explicit "off" turns the loop off, because a
// credential that is never renewed falls back to one model within minutes.
func TestGeminiWebRotationOptOut(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "", want: true},
		{value: "off", want: false},
		{value: "OFF", want: false},
		{value: " off ", want: false},
		{value: "0", want: false},
		{value: "false", want: false},
		{value: "no", want: false},
		{value: "disabled", want: false},
		{value: "on", want: true},
		{value: "1", want: true},
		{value: "true", want: true},
	}
	for _, tt := range tests {
		t.Run("value="+tt.value, func(t *testing.T) {
			t.Setenv(geminiWebRotationEnv, tt.value)
			if got := geminiWebRotationEnabled(); got != tt.want {
				t.Errorf("geminiWebRotationEnabled() with %s=%q = %v, want %v",
					geminiWebRotationEnv, tt.value, got, tt.want)
			}
		})
	}
}
