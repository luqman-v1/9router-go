package executor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/proxy/oauth"
)

func TestAutoclawSignerHeaders(t *testing.T) {
	fixed := int64(1730000000)
	oldNow := now
	now = func() int64 { return fixed }
	defer func() { now = oldNow }()

	ts := fmt.Sprintf("%d", fixed)
	wantSign := md5Sum(autoclawAppID + "&" + ts + "&" + autoclawAppKey)

	headers := autoclawHeaders("at-1", true, "zai_glm-5.3-flash")

	if headers["x-auth-appid"] != "100003" {
		t.Errorf("x-auth-appid = %q, want 100003", headers["x-auth-appid"])
	}
	if headers["x-auth-timestamp"] != ts {
		t.Errorf("x-auth-timestamp = %q, want %q", headers["x-auth-timestamp"], ts)
	}
	if headers["x-auth-sign"] != wantSign {
		t.Errorf("x-auth-sign = %q, want %q", headers["x-auth-sign"], wantSign)
	}
	if headers["x-product"] != "autoclaw" {
		t.Errorf("x-product = %q", headers["x-product"])
	}
	if headers["x-version"] != "1.18.5" {
		t.Errorf("x-version = %q", headers["x-version"])
	}
	if headers["x-tm"] != "win" {
		t.Errorf("x-tm = %q", headers["x-tm"])
	}
	if headers["x-client-type"] != "pc" {
		t.Errorf("x-client-type = %q", headers["x-client-type"])
	}
	if headers["x-channel"] != "official" {
		t.Errorf("x-channel = %q", headers["x-channel"])
	}
	if headers["x-lang"] != "zh-CN" {
		t.Errorf("x-lang = %q", headers["x-lang"])
	}
	if headers["Authorization"] != "Bearer at-1" {
		t.Errorf("authorization = %q, want Bearer at-1", headers["Authorization"])
	}
	if headers["X-Request-Model"] != "zai_glm-5.3-flash" {
		t.Errorf("X-Request-Model = %q, want zai_glm-5.3-flash", headers["X-Request-Model"])
	}
	if headers["Accept"] != "text/event-stream" {
		t.Errorf("stream Accept = %q, want text/event-stream", headers["Accept"])
	}
	if headers["Accept-Encoding"] != "identity" {
		t.Errorf("stream Accept-Encoding = %q, want identity", headers["Accept-Encoding"])
	}

	// No stream: Accept stays */*, no Accept-Encoding forced.
	headers = autoclawHeaders("", false, "")
	if headers["Accept"] != "*/*" {
		t.Errorf("non-stream Accept = %q, want */*", headers["Accept"])
	}
	if _, ok := headers["Accept-Encoding"]; ok {
		t.Errorf("non-stream should not force Accept-Encoding, got %q", headers["Accept-Encoding"])
	}
}

func TestAutoclawSignerIsDeterministic(t *testing.T) {
	fixed := int64(1730000000)
	oldNow := now
	now = func() int64 { return fixed }
	defer func() { now = oldNow }()

	h1 := autoclawHeaders("k", false, "")
	h2 := autoclawHeaders("k", false, "")
	if h1["x-auth-sign"] != h2["x-auth-sign"] {
		t.Errorf("signer not deterministic for fixed timestamp")
	}
	if h1["x-trace-id"] == h2["x-trace-id"] {
		t.Errorf("trace id should differ even with fixed time")
	}
}

func TestAutoclawBodyForcesStream(t *testing.T) {
	body := []byte(`{"model":"zai_glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`)
	out, err := autoclawBody(body, true)
	if err != nil {
		t.Fatalf("autoclawBody: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["stream"] != true {
		t.Errorf("stream = %v, want true", m["stream"])
	}
	if m["model"] != "zai_glm-5.3-flash" {
		t.Errorf("model = %v, want preserved", m["model"])
	}
}

// md5Sum is defined in autoclaw.go (same package); tests use it directly.

func TestAutoclawExecutorWireShape(t *testing.T) {
	var gotModelHeader string
	var gotAuth string
	var gotStream bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Token refresh endpoint
		if r.URL.Path == "/userapi/v1/refresh" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"at-2","refresh_token":"rt-2"}}`))
			return
		}
		// Chat endpoint
		gotModelHeader = r.Header.Get("X-Request-Model")
		gotAuth = r.Header.Get("Authorization")
		var b map[string]any
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Errorf("decode body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if v, ok := b["stream"]; ok {
			gotStream = v == true
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"))
	}))
	defer srv.Close()

	// Point both the chat endpoint and the token refresh endpoint at fakes.
	oldBase := autoclawExecutorBaseURL
	autoclawExecutorBaseURL = srv.URL
	defer func() { autoclawExecutorBaseURL = oldBase }()

	oldRefreshBase := oauth.SetAutoclawBaseURLForTest(srv.URL)
	defer func() { oauth.SetAutoclawBaseURLForTest(oldRefreshBase) }()

	req := &Request{
		Ctx:       t.Context(),
		Client:    http.DefaultClient,
		Provider:  "autoclaw",
		APIKey:    "rt-1|did-1",
		Body:      []byte(`{"model":"zai_glm-5.3-flash","stream":false}`),
		IsStream:  true,
		ModelName: "zai_glm-5.3-flash",
	}

	w := httptest.NewRecorder()
	if err := ForwardAutoclaw(w, req); err != nil {
		t.Fatalf("ForwardAutoclaw: %v", err)
	}

	if gotModelHeader != "zai_glm-5.3-flash" {
		t.Errorf("X-Request-Model = %q, want zai_glm-5.3-flash", gotModelHeader)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ") {
		t.Errorf("authorization = %q, want Bearer <token>", gotAuth)
	}
	if !gotStream {
		t.Errorf("body stream not forced to true")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "hi") {
		t.Errorf("response body missing streamed content: %s", w.Body.String())
	}
}

// TestAutoclawExecutor401RetryHitsOverrideBaseURL covers the 401 path the
// wire-shape test does not: a stale access token makes the first chat request
// 401, the executor force-refreshes once, and the retry goes to the same
// overridable base URL as the initial call — never to the production host.
// It also proves the retry path is reachable: with DoRequest the 401 became
// an *UpstreamError before the branch could run, so the forced refresh never
// fired (the review's "no test covering the retry path" hid a dead branch).
func TestAutoclawExecutor401RetryHitsOverrideBaseURL(t *testing.T) {
	oauth.ResetRefreshCache()

	var chatCalls, refreshCalls int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/userapi/v1/refresh":
			atomic.AddInt32(&refreshCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"code":0,"data":{"access_token":"at-call-%d","refresh_token":"rt-2"}}`, atomic.LoadInt32(&refreshCalls))
		case autoclawChatPath:
			n := atomic.AddInt32(&chatCalls, 1)
			if n == 1 {
				http.Error(w, `{"code":401,"message":"token expired"}`, http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"retry-ok"}}]}` + "\n\n"))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
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
		APIKey:    fmt.Sprintf("rt-%d|%s", 42, "device-9"),
		Body:      []byte(`{"model":"zai_glm-5.3-flash","stream":false}`),
		IsStream:  true,
		ModelName: "zai_glm-5.3-flash",
	}

	w := httptest.NewRecorder()
	if err := ForwardAutoclaw(w, req); err != nil {
		t.Fatalf("ForwardAutoclaw: %v", err)
	}

	if got := atomic.LoadInt32(&chatCalls); got != 2 {
		t.Errorf("chat calls = %d, want 2 (initial + one retry)", got)
	}
	if got := atomic.LoadInt32(&refreshCalls); got < 1 {
		t.Errorf("refresh calls = %d, want >= 1 (forced refresh before retry)", got)
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 after retry", w.Code)
	}
	if !strings.Contains(w.Body.String(), "retry-ok") {
		t.Errorf("retry response body missing content: %s", w.Body.String())
	}
}
