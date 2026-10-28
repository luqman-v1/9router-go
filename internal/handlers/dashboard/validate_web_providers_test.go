package dashboard

import (
	"net/http"
	"strings"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/proxy/executor"
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
//
// The set is the one a CHAT call carries — the token exchange is a chat call,
// and the site issues the chat calls below the interceptor that adds the
// `x-client-*` block. A probe that carried the block would be judging the
// credential under headers the real turn never sends.
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
	for header, want := range map[string]string{
		"Sec-Fetch-Mode": "cors",
		"Sec-Fetch-Site": "same-origin",
		"Referer":        "https://chat.deepseek.com/",
		"User-Agent":     executor.DeepSeekWebUserAgent(),
	} {
		if got := probe.headers.Get(header); got != want {
			t.Errorf("probe %s = %q, want %q", header, got, want)
		}
	}
	// The client block and the client hints belong to the settings and
	// telemetry calls, not to the chat ones.
	for _, header := range []string{"X-Client-Bundle-Id", "X-Client-Version", "X-Client-Platform", "Sec-Ch-Ua-Platform"} {
		if got := probe.headers.Get(header); got != "" {
			t.Errorf("probe %s = %q, want it absent — no browser sends it on a chat call", header, got)
		}
	}
	// The token exchange is a GET, and the site leaves the origin off its GETs.
	if got := probe.headers.Get("Origin"); got != "" {
		t.Errorf("probe Origin = %q, want it absent — the token exchange is a GET", got)
	}
}

// The site's own cookie travels with the probe when there is one: the WAF
// reads it, and a probe that omits it judges the credential under different
// conditions than the turn will meet.
func TestValidateDeepSeekWebSendsTheConfiguredCookie(t *testing.T) {
	calls := withProbeStub(t, func(_ int, _ capturedProbe) (int, []byte, error) {
		return http.StatusOK, deepSeekProbeBody("access-token"), nil
	})

	postValidate(t, `{"provider":"deepseek-web","apiKey":"user-token-1",`+
		`"providerSpecificData":{"deepSeekCookie":"ds_session_id=abc; aws-waf-token=def"}}`)

	if len(*calls) != 1 {
		t.Fatalf("expected one probe, got %d", len(*calls))
	}
	if got := (*calls)[0].headers.Get("Cookie"); !strings.Contains(got, "aws-waf-token=def") {
		t.Errorf("probe Cookie = %q, want the site's own cookie", got)
	}
}

// TestValidateGeminiWebProbesTheSessionAndReportsAStaleCookie proves the
// gemini-web probe distinguishes a working cookie from a refused one.
//
// The site reports a rejected session inside an HTTP-200 body, so a probe that
// read the status alone would call every dead cookie valid — the failure the
// user then sees is a chat request that returns nothing.
func TestValidateGeminiWebProbesTheSessionAndReportsAStaleCookie(t *testing.T) {
	calls := withProbeStub(t, func(call int, p capturedProbe) (int, []byte, error) {
		if !strings.Contains(p.url, "batchexecute") {
			t.Errorf("probe url = %q, want the model registry RPC", p.url)
		}
		if !strings.Contains(p.body, "otAQ7b") {
			t.Errorf("probe body = %q, want the otAQ7b RPC", p.body)
		}
		if got := p.headers.Get("Cookie"); !strings.Contains(got, "__Secure-1PSID=cookie-value") {
			t.Errorf("probe cookie = %q, want the pasted credential", got)
		}
		return http.StatusOK, []byte(`[null,false,false,1016]`), nil
	})

	rec, out := postValidate(t, `{"provider":"gemini-web","apiKey":"__Secure-1PSID=cookie-value"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if out["valid"] != false {
		t.Errorf("valid = %v, want false for a session the site refuses", out["valid"])
	}
	if msg := errorMessage(t, out); !strings.Contains(msg, "__Secure-1PSID") {
		t.Errorf("message = %q, want it to name the cookie to re-copy", msg)
	}
	if len(*calls) != 1 {
		t.Errorf("probes = %d, want 1", len(*calls))
	}
}

// TestValidateGeminiWebDoesNotMistakeTheRegistryConstantForARejection is the
// regression test for a real bug: a live anonymous registry carries the value
// 1016 — the same number the site's failure envelope uses — so a probe that
// scanned the body for it reported every working session as a refused one.
//
// Caught by probing the live site, which is the only place it showed.
func TestValidateGeminiWebDoesNotMistakeTheRegistryConstantForARejection(t *testing.T) {
	withProbeStub(t, func(_ int, _ capturedProbe) (int, []byte, error) {
		// The head of a live anonymous registry: the constant is at index 14 of
		// the payload and the model list follows.
		return http.StatusOK, []byte(`[["wrb.fr","otAQ7b","[6,[false,null,false],false,null,null,null,false,null,null,null,null,null,false,false,1016,[[\"cf41b0e0dd7d53e5\",\"Flash-Lite\"]]]"]]`), nil
	})

	rec, out := postValidate(t, `{"provider":"gemini-web","apiKey":"__Secure-1PSID=cookie-value; __Secure-1PSIDTS=sidts-value"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if out["valid"] != true {
		t.Errorf("valid = %v, want true: 1016 is an ordinary value inside a healthy registry (error: %v)",
			out["valid"], out["error"])
	}
}

// TestValidateGeminiWebAcceptsAWorkingCookie proves a model registry is read as a
// valid credential rather than as an unparsed body.
func TestValidateGeminiWebAcceptsAWorkingCookie(t *testing.T) {
	withProbeStub(t, func(_ int, _ capturedProbe) (int, []byte, error) {
		return http.StatusOK, []byte(`[["wrb.fr","otAQ7b","[6,[],[],[[\"cf41b0e0dd7d53e5\",\"Flash-Lite\"]]]"]]`), nil
	})

	rec, out := postValidate(t, `{"provider":"gemini-web","apiKey":"__Secure-1PSID=cookie-value; __Secure-1PSIDTS=sidts-value"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if out["valid"] != true {
		t.Errorf("valid = %v, want true for a session the site answers (error: %v)", out["valid"], out["error"])
	}
}

// TestValidateGeminiWebWithoutACookieSaysItStillWorks proves an empty credential
// is reported as usable rather than as a missing one.
//
// The site serves an anonymous visitor, so a connection with no cookie routes
// fine. Reporting it as "missing credential" would send the user looking for one
// they do not need.
func TestValidateGeminiWebWithoutACookieSaysItStillWorks(t *testing.T) {
	calls := withProbeStub(t, func(_ int, _ capturedProbe) (int, []byte, error) {
		t.Fatal("an empty credential has nothing to probe")
		return 0, nil, nil
	})

	rec, out := postValidate(t, `{"provider":"gemini-web","apiKey":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if out["valid"] != true {
		t.Errorf("valid = %v, want true: gemini-web works without a cookie", out["valid"])
	}
	if msg := errorMessage(t, out); msg != "" {
		t.Errorf("an anonymous connection must not be reported as an error; got %q", msg)
	}
	if len(*calls) != 0 {
		t.Errorf("expected no outbound probe, got %d", len(*calls))
	}
}

// TestValidateGeminiWebSignsItsRegistryProbe proves the probe asks the
// registry the way a signed-in page asks it.
//
// The registry RPC is the call that decides which models a session may use,
// and the site answers it as an anonymous visitor when the call is unsigned.
// An unsigned probe therefore reports a working Pro account as reaching one
// model — and a user who pastes a correct cookie is told to paste it again.
func TestValidateGeminiWebSignsItsRegistryProbe(t *testing.T) {
	withProbeStub(t, func(_ int, p capturedProbe) (int, []byte, error) {
		auth := p.headers.Get("Authorization")
		if !strings.HasPrefix(auth, "SAPISIDHASH ") {
			t.Errorf("probe authorization = %q, want a SAPISIDHASH signature", auth)
		}
		return http.StatusOK, []byte(`[["wrb.fr","otAQ7b","[6,[],[],[[\"cf41b0e0dd7d53e5\",\"Flash-Lite\"]]]"]]`), nil
	})

	rec, out := postValidate(t, `{"provider":"gemini-web","apiKey":"__Secure-1PSID=psid; __Secure-1PSIDTS=sidts-value; SAPISID=sapisid-value"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if out["valid"] != true {
		t.Errorf("valid = %v, want true for a session the site answers (error: %v)", out["valid"], out["error"])
	}
}

// TestValidateGeminiWebReportsACookieWithoutTheSessionTimestamp proves the
// probe says what is actually wrong with a credential that carries no
// __Secure-1PSIDTS.
//
// That cookie is what carries the session into the model registry, and a
// header without it is answered as a visitor's however fresh the rest of it
// is. Telling the user to re-copy the whole cookie sends them round a loop
// that ends where it started.
func TestValidateGeminiWebReportsACookieWithoutTheSessionTimestamp(t *testing.T) {
	withProbeStub(t, func(_ int, p capturedProbe) (int, []byte, error) {
		return http.StatusOK, []byte(`[["wrb.fr","otAQ7b","[6,[],[],[[\"cf41b0e0dd7d53e5\",\"Flash-Lite\"]]]"]]`), nil
	})

	rec, out := postValidate(t, `{"provider":"gemini-web","apiKey":"__Secure-1PSID=psid-only"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if out["valid"] != false {
		t.Errorf("valid = %v, want false: the credential cannot reach the signed-in models", out["valid"])
	}
	if msg := errorMessage(t, out); !strings.Contains(msg, "__Secure-1PSIDTS") {
		t.Errorf("message = %q, want it to name the cookie that is missing", msg)
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
