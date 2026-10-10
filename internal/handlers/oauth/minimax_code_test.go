package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sha256Sum is the digest the PKCE challenge is computed from.
func sha256Sum(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// timeAt is a fixed instant, so the millisecond-epoch case does not depend on
// when the suite runs.
func timeAt(t *testing.T) time.Time {
	t.Helper()
	base, err := time.Parse(time.RFC3339, "2026-01-02T03:04:05Z")
	if err != nil {
		t.Fatalf("parse fixed instant: %v", err)
	}
	return base
}

// itoa renders an int64 for the JSON fixtures above.
func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// parseJSONMap decodes a fixture body.
func parseJSONMap(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("fixture is not JSON: %v", err)
	}
	return m
}

// mcodeServer answers both halves of the mcode device grant and records the
// form each one received.
type mcodeServer struct {
	srv *httptest.Server
	// deviceForm is the POST to /oauth2/device/code.
	deviceForm url.Values
	// tokenForms holds every POST to /oauth2/token, in order.
	tokenForms []url.Values
}

func newMCodeServer(t *testing.T, deviceBody, tokenBody string, tokenStatus int) *mcodeServer {
	t.Helper()
	m := &mcodeServer{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth2/device/code":
			m.deviceForm = r.PostForm
			_, _ = w.Write([]byte(deviceBody))
		case "/oauth2/token":
			m.tokenForms = append(m.tokenForms, r.PostForm)
			w.WriteHeader(tokenStatus)
			_, _ = w.Write([]byte(tokenBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(m.srv.Close)
	miniMaxCodeSites["minimax-code"] = miniMaxCodeSite{accountBase: m.srv.URL}
	t.Cleanup(func() { miniMaxCodeSites["minimax-code"] = miniMaxCodeSite{accountBase: "https://account.minimax.cn"} })
	return m
}

// The device-code request must carry the mcode client, its scope and audience,
// and a real PKCE S256 challenge — the token endpoint rejects the exchange
// without the matching verifier.
func TestMiniMaxCodeStart_SendsPKCEChallenge(t *testing.T) {
	m := newMCodeServer(t, `{"device_code":"dev-1","user_code":"ABCD-1234","verification_uri":"https://account.minimax.cn/activate","expires_in":300,"interval":5}`, `{}`, http.StatusOK)

	out, err := minimaxCodeStart("minimax-code")
	if err != nil {
		t.Fatalf("minimaxCodeStart: %v", err)
	}
	for field, want := range map[string]string{
		"client_id":             "mcode-public",
		"scope":                 "agent.default",
		"audience":              "agent-backend",
		"code_challenge_method": "S256",
	} {
		if got := m.deviceForm.Get(field); got != want {
			t.Errorf("%s = %q, want %q", field, got, want)
		}
	}

	challenge := m.deviceForm.Get("code_challenge")
	if challenge == "" {
		t.Fatal("no code_challenge sent")
	}
	// The challenge has to be the S256 digest of the verifier the session
	// carries, not some other value: the poll presents that verifier and
	// MiniMax recomputes the challenge from it.
	session, _ := out["session"].(map[string]any)
	verifier, _ := session["codeVerifier"].(string)
	if verifier == "" {
		t.Fatal("the session carries no PKCE verifier for the poll to present")
	}
	sum := sha256Sum(verifier)
	if got := base64.RawURLEncoding.EncodeToString(sum); got != challenge {
		t.Errorf("code_challenge %q is not the S256 digest of the session verifier (%q)", challenge, got)
	}

	if out["device_code"] != "dev-1" {
		t.Errorf("device_code = %v, want dev-1", out["device_code"])
	}
	if out["user_code"] != "ABCD-1234" {
		t.Errorf("user_code = %v, want ABCD-1234", out["user_code"])
	}
	if out["verification_uri"] != "https://account.minimax.cn/activate" {
		t.Errorf("verification_uri = %v", out["verification_uri"])
	}
	if out["expires_in"] != 300 {
		t.Errorf("expires_in = %v, want 300", out["expires_in"])
	}
	if out["interval"] != 5 {
		t.Errorf("interval = %v, want 5 (seconds on the standard shape)", out["interval"])
	}
}

// MiniMax sometimes answers the user-code variant: no device_code, and an
// interval in milliseconds. The poll must then be switched onto the user code,
// which the modal reaches as device_code.
func TestMiniMaxCodeStart_FallsBackToUserCodePolling(t *testing.T) {
	body := `{"user_code":"XYZ-9","verification_uri":"https://account.minimax.cn/activate","interval":1500}`
	m := newMCodeServer(t, body, `{}`, http.StatusOK)

	out, err := minimaxCodeStart("minimax-code")
	if err != nil {
		t.Fatalf("minimaxCodeStart: %v", err)
	}
	if out["device_code"] != "XYZ-9" {
		t.Errorf("device_code = %v, want the user code XYZ-9", out["device_code"])
	}
	if out["interval"] != 2 {
		t.Errorf("interval = %v, want 2 seconds (the reply stated 1500 ms)", out["interval"])
	}
	session, _ := out["session"].(map[string]any)
	if pollByUserCode(session) != true {
		t.Error("the session does not record that this is the user-code variant")
	}
	if m.deviceForm == nil {
		t.Fatal("no device-code request was sent")
	}
}

// A millisecond-epoch expiry is normalized to seconds remaining, and a reply
// with none falls back to the 10-minute sign-in window.
func TestMiniMaxCodeDeadline(t *testing.T) {
	now := timeAt(t)
	tests := []struct {
		name string
		body string
		want int
	}{
		{"expires_in wins", `{"expires_in":300}`, 300},
		{"plain seconds", `{"expired_in":120}`, 120},
		{"millisecond epoch", `{"expired_in":` + itoa(now.UnixMilli()+90_000) + `}`, 90},
		{"nothing stated", `{}`, miniMaxCodeSignInSeconds},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := parseJSONMap(t, tt.body)
			if got := miniMaxCodeDeadline(data, now); got != tt.want {
				t.Errorf("miniMaxCodeDeadline = %d, want %d", got, tt.want)
			}
		})
	}
}

// The poll's status envelope must map onto the framework's pending/error
// answers, or the modal would sit on "waiting" after the user finished.
func TestMiniMaxCodePoll_StatusEnvelope(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"pending", `{"status":"pending"}`, "authorization_pending"},
		{"slow down", `{"status":"slow_down"}`, "slow_down"},
		{"denied", `{"status":"denied"}`, "access_denied"},
		{"expired", `{"status":"expired"}`, "expired_token"},
		{"unknown status", `{"status":"weird"}`, "unexpected_status:weird"},
		{"oauth error body", `{"error":"invalid_request"}`, "invalid_request"},
		{"no token", `{}`, "no access_token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newMCodeServer(t, `{}`, tt.body, http.StatusOK)

			_, err := minimaxCodePoll("minimax-code", "dev-1", map[string]any{})
			if err == nil {
				t.Fatalf("%s produced no error", tt.name)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to name %q", err, tt.wantErr)
			}
		})
	}
}

// The user-code variant posts user_code and no device_code; the standard one
// posts device_code and no user_code.
func TestMiniMaxCodePoll_PostsTheRightCodeField(t *testing.T) {
	m := newMCodeServer(t, `{}`, `{"access_token":"at","refresh_token":"rt","expires_in":3600}`, http.StatusOK)

	tokens, err := minimaxCodePoll("minimax-code", "XYZ-9",
		map[string]any{miniMaxCodePollByUser: true, "codeVerifier": "v"})
	if err != nil {
		t.Fatalf("user-code poll: %v", err)
	}
	if tokens.access != "at" || tokens.refresh != "rt" || tokens.expiresIn != 3600 {
		t.Errorf("tokens = %+v, want the issued at/rt/3600", tokens)
	}
	form := m.tokenForms[0]
	if form.Get("user_code") != "XYZ-9" || form.Get("device_code") != "" {
		t.Errorf("user-code variant sent user_code=%q device_code=%q", form.Get("user_code"), form.Get("device_code"))
	}
	if form.Get("grant_type") != miniMaxCodeGrantType {
		t.Errorf("grant_type = %q, want %q", form.Get("grant_type"), miniMaxCodeGrantType)
	}
	if form.Get("code_verifier") != "v" {
		t.Errorf("code_verifier = %q, want the session's v", form.Get("code_verifier"))
	}

	m = newMCodeServer(t, `{}`, `{"status":"pending"}`, http.StatusOK)
	if _, err := minimaxCodePoll("minimax-code", "dev-1", map[string]any{}); err == nil {
		t.Fatal("a pending answer produced no error")
	}
	form = m.tokenForms[0]
	if form.Get("device_code") != "dev-1" || form.Get("user_code") != "" {
		t.Errorf("standard variant sent device_code=%q user_code=%q", form.Get("device_code"), form.Get("user_code"))
	}
}

// Both site providers must be dispatchable, and nothing else may be accepted by
// the shared device handler.
func TestMiniMaxCode_DeviceFlowRegistration(t *testing.T) {
	for _, provider := range []string{"minimax-code", "minimax-code-global"} {
		if !deviceSupported(provider) {
			t.Errorf("%s is not accepted by the device-flow handler", provider)
		}
	}
	if deviceSupported("minimax") {
		t.Error("the API-key minimax provider was accepted by the mcode device flow")
	}
	// The two ids are their own canonical forms; neither is an alias of the other.
	if got := deviceCanonical("minimax-code-global"); got != "minimax-code-global" {
		t.Errorf("deviceCanonical(minimax-code-global) = %q, want it unchanged", got)
	}
}
