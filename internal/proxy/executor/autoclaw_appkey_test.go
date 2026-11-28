package executor

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/proxy/oauth"
)

// TestAutoclawExecutorSignsWithOperatorSuppliedAppKey pins that the request
// signature comes from the operator's credential, not from a constant in this
// tree. The pre-fix head signed with an embedded third-party app key, so its
// signature does not match the operator's key and this test fails there.
func TestAutoclawExecutorSignsWithOperatorSuppliedAppKey(t *testing.T) {
	oauth.ResetRefreshCache()

	fixed := int64(1730000000)
	oldNow := now
	now = func() int64 { return fixed }
	defer func() { now = oldNow }()

	const appKey = "operator-app-key"
	var gotSign, gotAppID string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/userapi/v1/refresh" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"at-2","refresh_token":"rt-2"}}`))
			return
		}
		gotSign = r.Header.Get("x-auth-sign")
		gotAppID = r.Header.Get("x-auth-appid")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"ok"}}]}` + "\n\n"))
	}))
	defer srv.Close()

	oldBase := autoclawExecutorBaseURL
	autoclawExecutorBaseURL = srv.URL
	defer func() { autoclawExecutorBaseURL = oldBase }()

	oldRefreshBase := oauth.SetAutoclawBaseURLForTest(srv.URL)
	defer func() { oauth.SetAutoclawBaseURLForTest(oldRefreshBase) }()

	req := &Request{
		Ctx:       t.Context(),
		Client:    http.DefaultClient,
		Provider:  "autoclaw",
		APIKey:    "rt-test|device-9|" + appKey,
		Body:      []byte(`{"model":"zai_glm-5.3-flash","stream":true}`),
		IsStream:  true,
		ModelName: "zai_glm-5.3-flash",
	}

	w := httptest.NewRecorder()
	if err := ForwardAutoclaw(w, req); err != nil {
		t.Fatalf("ForwardAutoclaw: %v", err)
	}

	want := md5Sum("100003" + "&" + fmt.Sprintf("%d", fixed) + "&" + appKey)
	if gotSign != want {
		t.Errorf("x-auth-sign = %q, want %q (the operator's app key must sign the request)", gotSign, want)
	}
	if gotAppID != "100003" {
		t.Errorf("x-auth-appid = %q, want 100003", gotAppID)
	}
}

// TestAutoclawExecutorUsesConstantAppKey pins that a credential without
// the app key is refused with the expected format instead of being signed
// with a key from this tree (the pre-fix head carried one, so it happily
// signed the request) or reaching upstream at all.
func TestAutoclawExecutorUsesConstantAppKey(t *testing.T) {
	oauth.ResetRefreshCache()

	var chatCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/userapi/v1/refresh" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"at-2","refresh_token":"rt-2"}}`))
			return
		}
		atomic.AddInt32(&chatCalls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"ok"}}]}` + "\n\n"))
	}))
	defer srv.Close()

	oldBase := autoclawExecutorBaseURL
	autoclawExecutorBaseURL = srv.URL
	defer func() { autoclawExecutorBaseURL = oldBase }()

	oldRefreshBase := oauth.SetAutoclawBaseURLForTest(srv.URL)
	defer func() { oauth.SetAutoclawBaseURLForTest(oldRefreshBase) }()

	req := &Request{
		Ctx:       t.Context(),
		Client:    http.DefaultClient,
		Provider:  "autoclaw",
		APIKey:    "rt-test|device-9",
		Body:      []byte(`{"model":"zai_glm-5.3-flash","stream":true}`),
		IsStream:  true,
		ModelName: "zai_glm-5.3-flash",
	}
	w := httptest.NewRecorder()
	err := ForwardAutoclaw(w, req)
	if err != nil {
		t.Fatalf("ForwardAutoclaw: %v", err)
	}
	if got := atomic.LoadInt32(&chatCalls); got != 1 {
		t.Errorf("chat calls = %d, want 1 (OAuth credentials omit the app key; the constant signs)", got)
	}
}
