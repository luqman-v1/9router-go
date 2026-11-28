package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestAutoclawOAuthAuthorizeAndExchange(t *testing.T) {
	oldOrigin := autoclawOAuthOrigin
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/userapi/overseasv1/zai-oauth-url":
			if in["source_id"] != "autoclaw" {
				t.Errorf("source_id = %v", in["source_id"])
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"oauth_url":"https://chat.z.ai/oauth/authorize?state=abc","state":"abc"}}`))
		case "/userapi/overseasv1/zai-oauth-login":
			if in["code"] != "code-1" || in["state"] != "abc" {
				t.Errorf("exchange body = %v", in)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"user_id":"u-1","access_token":"at-1","refresh_token":"rt-1"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	autoclawOAuthOrigin = srv.URL
	defer func() { autoclawOAuthOrigin = oldOrigin }()

	h := &OAuthHandler{Repo: nil}
	rec := httptest.NewRecorder()
	h.HandleAutoclawAuthorize(rec, httptest.NewRequest(http.MethodGet, "/api/oauth/autoclaw/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A20130%2Fcallback", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "chat.z.ai/oauth/authorize") {
		t.Errorf("authorize body = %s", rec.Body.String())
	}
}

func TestAutoclawCaptchaConfigProofAndRedirectURI(t *testing.T) {
	oldOrigin := autoclawOAuthOrigin
	var proof, navigateURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case autoclawCaptchaConfigPath:
			_, _ = w.Write([]byte(`{"code":0,"data":{"enabled":true,"region":"ga","prefix":"sq51tr","scene_id":"18vhnjxl"}}`))
		case "/userapi/overseasv1/zai-oauth-url":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			proof, _ = in["ali_captcha_verify_param"].(string)
			navigateURI, _ = in["navigate_uri"].(string)
			_, _ = w.Write([]byte(`{"code":0,"data":{"oauth_url":"https://chat.z.ai/oauth/authorize?state=abc","state":"abc"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	autoclawOAuthOrigin = srv.URL
	defer func() { autoclawOAuthOrigin = oldOrigin }()

	h := &OAuthHandler{}
	cfgRec := httptest.NewRecorder()
	h.HandleAutoclawCaptchaConfig(cfgRec, httptest.NewRequest(http.MethodGet, "/api/oauth/autoclaw/captcha-config", nil))
	if cfgRec.Code != http.StatusOK {
		t.Fatalf("captcha-config status = %d: %s", cfgRec.Code, cfgRec.Body.String())
	}
	if !strings.Contains(cfgRec.Body.String(), "18vhnjxl") {
		t.Errorf("captcha-config body = %s", cfgRec.Body.String())
	}

	authRec := httptest.NewRecorder()
	h.HandleAutoclawAuthorize(authRec, httptest.NewRequest(http.MethodGet, "/api/oauth/autoclaw/authorize?captcha_proof=proof-123", nil))
	if authRec.Code != http.StatusOK {
		t.Fatalf("authorize status = %d: %s", authRec.Code, authRec.Body.String())
	}
	if proof != "proof-123" {
		t.Errorf("ali_captcha_verify_param = %q, want proof-123", proof)
	}
	wantURI := "http://" + autoclawLoopbackHost + ":" + strconv.Itoa(autoclawLoopbackPort) + autoclawLoopbackPath
	if navigateURI != wantURI {
		t.Errorf("navigate_uri = %q, want %q", navigateURI, wantURI)
	}
}

func TestAutoclawUserEmail(t *testing.T) {
	oldOrigin := autoclawOAuthOrigin
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/userapi/v1/user-profile" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["source_id"] != "autoclaw" || in["device_id"] != "device-1" {
			t.Errorf("profile body = %v", in)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer at-1" {
			t.Errorf("Authorization = %q, want Bearer at-1", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"email":"user@example.com","user_id":"u-1"}}`))
	}))
	defer srv.Close()
	autoclawOAuthOrigin = srv.URL
	defer func() { autoclawOAuthOrigin = oldOrigin }()

	if got := autoclawUserEmail(t.Context(), "device-1", "Bearer at-1"); got != "user@example.com" {
		t.Errorf("autoclawUserEmail = %q, want user@example.com", got)
	}
}
