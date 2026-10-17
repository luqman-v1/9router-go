package dashboard

import (
	"net/http"
	"strings"
	"testing"

	json "encoding/json/v2"
)

// deepSeekProbeBody builds the users/current envelope the probe reads. The web
// API nests the access token under data.biz_data.
func deepSeekProbeBody(token string) []byte {
	return mustMarshalJSON(map[string]any{
		"code": 0,
		"data": map[string]any{"biz_data": map[string]any{"token": token}},
	})
}

// deepSeekProbeErrorBody builds the HTTP-200-with-a-code rejection this API uses
// for a dead credential.

// deepSeekProbeMuteBody is the account-level enforcement, which this API answers
// with HTTP 200: the credential is fine and the account is punished.
func deepSeekProbeMuteBody() []byte {
	return mustMarshalJSON(map[string]any{
		"code": 0,
		"data": map[string]any{
			"biz_code": 5,
			"biz_msg":  "user is muted",
		},
	})
}

// deepSeekProbeErrorBody builds the HTTP-200-with-a-code rejection this API uses
// for a dead credential.
func deepSeekProbeErrorBody(code int) []byte {
	return mustMarshalJSON(map[string]any{"code": code, "msg": "Authorization Failed"})
}

func mustMarshalJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

// TestValidateDeepSeekWeb probes the browser-login credential exchange.
//
// The cases that matter are the ones where the HTTP status alone is
// misleading: this API answers HTTP 200 for a rejected credential and puts the
// real answer in a code. A probe that trusted the status would report a dead
// token as valid, and the user would only find out on their first real request.
func TestValidateDeepSeekWeb(t *testing.T) {
	tests := []struct {
		name        string
		apiKey      string
		respond     func(call int, p capturedProbe) (int, []byte, error)
		wantValid   bool
		wantMessage string
	}{
		{
			name:   "valid token",
			apiKey: "user-token-1",
			respond: func(int, capturedProbe) (int, []byte, error) {
				return http.StatusOK, deepSeekProbeBody("access-token"), nil
			},
			wantValid: true,
		},
		{
			name:   "json-wrapped localStorage value",
			apiKey: `{"value":"user-token-2"}`,
			respond: func(int, capturedProbe) (int, []byte, error) {
				return http.StatusOK, deepSeekProbeBody("access-token"), nil
			},
			wantValid: true,
		},
		{
			name:   "rejected credential arrives as HTTP 200",
			apiKey: "stale-token",
			respond: func(int, capturedProbe) (int, []byte, error) {
				return http.StatusOK, deepSeekProbeErrorBody(40003), nil
			},
			wantValid:   false,
			wantMessage: "userToken is invalid or expired",
		},
		{
			name:   "HTTP 401",
			apiKey: "stale-token",
			respond: func(int, capturedProbe) (int, []byte, error) {
				return http.StatusUnauthorized, nil, nil
			},
			wantValid:   false,
			wantMessage: "userToken is invalid or expired",
		},
		{
			name:   "HTTP 200 without a token is not a valid session",
			apiKey: "user-token-3",
			respond: func(int, capturedProbe) (int, []byte, error) {
				return http.StatusOK, mustMarshalJSON(map[string]any{"code": 0, "data": map[string]any{}}), nil
			},
			wantValid:   false,
			wantMessage: "no access token",
		},
		{
			name:   "missing credential",
			apiKey: "   ",
			respond: func(int, capturedProbe) (int, []byte, error) {
				t.Fatal("a probe with no credential must not reach upstream")
				return 0, nil, nil
			},
			wantValid:   false,
			wantMessage: "Missing userToken",
		},
		{
			name:   "a muted account is named, not reported as a dead token",
			apiKey: "user-token-4",
			respond: func(int, capturedProbe) (int, []byte, error) {
				// The enforcement arrives as HTTP 200 with the refusal in the
				// body, so a probe that trusted the status would call this
				// account valid and the user would find out on the first turn.
				return http.StatusOK, deepSeekProbeMuteBody(), nil
			},
			wantValid:   false,
			wantMessage: "refused this account",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := withProbeStub(t, tt.respond)
			rec, out := postValidate(t, `{"provider":"deepseek-web","apiKey":`+quoteJSON(tt.apiKey)+`}`)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
			}
			if out["valid"] != tt.wantValid {
				t.Errorf("valid = %v, want %v (error: %v)", out["valid"], tt.wantValid, out["error"])
			}
			if tt.wantMessage != "" {
				if msg := errorMessage(t, out); !strings.Contains(msg, tt.wantMessage) {
					t.Errorf("error = %q, want it to mention %q", msg, tt.wantMessage)
				}
			}
			_ = calls
		})
	}
}

// TestValidateDeepSeekWebSendsBrowserFingerprint pins the headers the probe
// sends. The credential is judged against those headers, so a probe that sent a
// generic Authorization header would be testing something the real site never
// sees.
func TestValidateDeepSeekWebSendsBrowserFingerprint(t *testing.T) {
	calls := withProbeStub(t, func(_ int, _ capturedProbe) (int, []byte, error) {
		return http.StatusOK, deepSeekProbeBody("access-token"), nil
	})

	postValidate(t, `{"provider":"deepseek-web","apiKey":"user-token-1"}`)

	if len(*calls) != 1 {
		t.Fatalf("expected one probe, got %d", len(*calls))
	}
	probe := (*calls)[0]
	if probe.url != deepSeekUsersCurrentURL {
		t.Errorf("probed %q, want %q", probe.url, deepSeekUsersCurrentURL)
	}
	if got := probe.headers.Get("Authorization"); got != "Bearer user-token-1" {
		t.Errorf("Authorization = %q, want the Bearer userToken", got)
	}
	// The probe must send the executor's own fingerprint, including the
	// client-attestation header family: a probe judged against a different
	// header set says nothing about whether the real turn will be accepted.
	for header, want := range map[string]string{
		"X-Client-Bundle-Id": "com.deepseek.chat",
		"X-Client-Version":   "2.3.0",
		"X-Client-Platform":  "web",
		"Origin":             "https://chat.deepseek.com",
		"Sec-Fetch-Mode":     "cors",
		"Sec-Ch-Ua-Platform": `"Windows"`,
	} {
		if got := probe.headers.Get(header); got != want {
			t.Errorf("probe %s = %q, want %q", header, got, want)
		}
	}
}

// TestValidateGeminiWebExplainsItCannotCheck proves the gemini-web probe reports
// why it cannot answer rather than calling the site.
//
// gemini.google.com issues its tokens from inside the browser, so any probe
// that called it would report a valid cookie as invalid. Saying so is the only
// answer the user can act on.
func TestValidateGeminiWebExplainsItCannotCheck(t *testing.T) {
	calls := withProbeStub(t, func(_ int, _ capturedProbe) (int, []byte, error) {
		t.Fatal("gemini-web must not probe the site from a build with no browser")
		return 0, nil, nil
	})

	rec, out := postValidate(t, `{"provider":"gemini-web","apiKey":"__Secure-1PSID=abc"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if out["valid"] != false {
		t.Errorf("valid = %v, want false: this build cannot prove the cookie works", out["valid"])
	}
	if len(*calls) != 0 {
		t.Errorf("expected no outbound probe, got %d", len(*calls))
	}
}

// quoteJSON renders a Go string as a JSON string literal, for the raw request
// bodies these tests post.
func quoteJSON(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
