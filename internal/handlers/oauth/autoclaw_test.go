package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
