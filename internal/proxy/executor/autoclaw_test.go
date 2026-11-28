package executor

import (
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/oauth"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// autoclawTestCredential is the credential shape the operator supplies:
// refresh_token|device_id|app_key.
func autoclawTestCredential() string { return "rt-test|device-9|app-key-1" }

func TestAutoclawSignerHeaders(t *testing.T) {
	fixed := int64(1730000000)
	oldNow := now
	now = func() int64 { return fixed }
	defer func() { now = oldNow }()

	ts := fmt.Sprintf("%d", fixed)
	wantSign := md5Sum(autoclawAppID + "&" + ts + "&" + "app-key-1")

	headers := autoclawHeaders("at-1", true, "zai_glm-5.3-flash", "app-key-1")

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
	if headers["x-version"] != "2.6.1" {
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
	if headers["x-lang"] != "en" {
		t.Errorf("x-lang = %q, want en", headers["x-lang"])
	}
	if headers["X-Authorization"] != "Bearer at-1" {
		t.Errorf("X-Authorization = %q, want Bearer at-1", headers["X-Authorization"])
	}
	if headers["X-Agent-Id"] != "main" {
		t.Errorf("X-Agent-Id = %q, want main", headers["X-Agent-Id"])
	}
	if headers["x-return-billing"] != "true" {
		t.Errorf("x-return-billing = %q, want true", headers["x-return-billing"])
	}
	if headers["x_trace_id"] != "autoclaw-desktop" {
		t.Errorf("x_trace_id = %q, want autoclaw-desktop", headers["x_trace_id"])
	}
	if headers["User-Agent"] != "node" {
		t.Errorf("User-Agent = %q, want node", headers["User-Agent"])
	}
	if headers["X-Request-Model"] != "zai_glm-5.3-flash" {
		t.Errorf("X-Request-Model = %q, want zai_glm-5.3-flash", headers["X-Request-Model"])
	}
	// The desktop client keeps application/json even on streaming calls;
	// text/event-stream here makes the identity gateway answer 406.
	if headers["Accept"] != "application/json" {
		t.Errorf("stream Accept = %q, want application/json", headers["Accept"])
	}
	if headers["Accept-Encoding"] != "br, gzip, deflate" {
		t.Errorf("stream Accept-Encoding = %q, want br, gzip, deflate", headers["Accept-Encoding"])
	}

	// Non-stream uses the same captured header set.
	headers = autoclawHeaders("", false, "", "app-key-1")
	if headers["Accept"] != "application/json" {
		t.Errorf("non-stream Accept = %q, want application/json", headers["Accept"])
	}
	if headers["Accept-Encoding"] != "br, gzip, deflate" {
		t.Errorf("non-stream Accept-Encoding = %q, want br, gzip, deflate", headers["Accept-Encoding"])
	}
}

func TestAutoclawSignerIsDeterministic(t *testing.T) {
	fixed := int64(1730000000)
	oldNow := now
	now = func() int64 { return fixed }
	defer func() { now = oldNow }()

	h1 := autoclawHeaders("k", false, "", "app-key-1")
	h2 := autoclawHeaders("k", false, "", "app-key-1")
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
		gotAuth = r.Header.Get("X-Authorization")
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
		APIKey:    autoclawTestCredential(),
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
		APIKey:    autoclawTestCredential(),
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

// TestAutoclawExecutorNonStreamFoldsSSEIntoJSON covers the non-stream lane a
// client actually sees: the upstream protocol is SSE-only, so a stream:false
// ask still comes back as frames. The executor must fold them into one
// chat.completion before labelling the body application/json — the reviewed
// defect relayed the raw frames under the JSON header, which no OpenAI
// client's parser can read. Replayed onto the pre-fix head this fails with
// "body is not JSON a client can parse" on the `data: {…}` prefix.
func TestAutoclawExecutorNonStreamFoldsSSEIntoJSON(t *testing.T) {
	oauth.ResetRefreshCache()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/userapi/v1/refresh" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"at-2","refresh_token":"rt-2"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1730000000,"model":"zai_glm-5.3-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"hello "}}]}` + "\n\n" +
				`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1730000000,"model":"zai_glm-5.3-flash","choices":[{"index":0,"delta":{"content":"world"}}]}` + "\n\n" +
				`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1730000000,"model":"zai_glm-5.3-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}` + "\n\n" +
				"data: [DONE]\n\n"))
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
		APIKey:    autoclawTestCredential(),
		Body:      []byte(`{"model":"zai_glm-5.3-flash","stream":false}`),
		IsStream:  false,
		ModelName: "zai_glm-5.3-flash",
	}

	w := httptest.NewRecorder()
	if err := ForwardAutoclaw(w, req); err != nil {
		t.Fatalf("ForwardAutoclaw: %v", err)
	}

	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var doc struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body is not JSON a client can parse (%v): %s", err, w.Body.String())
	}
	if doc.Object != "chat.completion" {
		t.Errorf("object = %q, want chat.completion", doc.Object)
	}
	if len(doc.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(doc.Choices))
	}
	if doc.Choices[0].Message.Content != "hello world" {
		t.Errorf("content = %q, want %q", doc.Choices[0].Message.Content, "hello world")
	}
	if doc.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", doc.Choices[0].FinishReason)
	}
	if doc.Usage.TotalTokens != 9 {
		t.Errorf("usage.total_tokens = %d, want 9", doc.Usage.TotalTokens)
	}
}

// TestAutoclawExecutorStreamFlushesBeforeUpstreamCloses covers the other
// reviewed defect: the relay copied the upstream body without flushing, so
// the client's first byte arrived only when the stream closed — TTFB equalled
// the whole stream duration and a chat answer appeared all at once. A real
// HTTP hop stands in for the client: the fake upstream sends the first frame
// at once, holds the stream open, then closes. The first byte must land
// while the stream is still open. On the pre-fix head the first read blocks
// until close, so the elapsed time equals (or exceeds) the hold.
func TestAutoclawExecutorStreamFlushesBeforeUpstreamCloses(t *testing.T) {
	oauth.ResetRefreshCache()

	const holdOpen = 400 * time.Millisecond

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/userapi/v1/refresh" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"at-2","refresh_token":"rt-2"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"first"}}]}` + "\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(holdOpen)
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":" last"},"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	oldBase := autoclawExecutorBaseURL
	autoclawExecutorBaseURL = upstream.URL
	defer func() { autoclawExecutorBaseURL = oldBase }()

	oldRefreshBase := oauth.SetAutoclawBaseURLForTest(upstream.URL)
	defer func() { oauth.SetAutoclawBaseURLForTest(oldRefreshBase) }()

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		if err := ForwardAutoclaw(w, &Request{
			Ctx:       r.Context(),
			Client:    http.DefaultClient,
			Provider:  "autoclaw",
			APIKey:    autoclawTestCredential(),
			Body:      body,
			IsStream:  true,
			ModelName: "zai_glm-5.3-flash",
		}); err != nil {
			t.Errorf("ForwardAutoclaw: %v", err)
		}
	}))
	defer gateway.Close()

	client := &http.Client{Timeout: 10 * time.Second}
	start := time.Now()
	resp, err := client.Post(gateway.URL+autoclawChatPath, "application/json",
		strings.NewReader(`{"model":"zai_glm-5.3-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	buf := make([]byte, 64)
	n, err := resp.Body.Read(buf)
	if err != nil && n == 0 {
		t.Fatalf("read first frame: %v", err)
	}
	firstByte := time.Since(start)

	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read rest of stream: %v", err)
	}
	body := string(buf[:n]) + string(rest)
	if !strings.Contains(body, "first") || !strings.Contains(body, " last") {
		t.Errorf("stream body missing frames: %s", body)
	}
	if firstByte >= holdOpen/2 {
		t.Errorf("first frame took %v while the upstream held the stream open %v: the relay buffered the whole stream", firstByte, holdOpen)
	}
}

func TestAutoclawExecutorHonoursConnectionBaseURL(t *testing.T) {
	// A saved connection with data.baseUrl must be used for both the chat
	// call and the refresh, not the compile-time constant.
	oldBase := autoclawExecutorBaseURL
	defer func() { autoclawExecutorBaseURL = oldBase }()
	var chatHits, refreshHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/userapi/v1/"):
			refreshHits++
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"at-2","refresh_token":"rt-2"}}`))
		default:
			chatHits++
			_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"ok"}}]}` + "\n\n" + "data: [DONE]\n\n"))
		}
	}))
	defer srv.Close()
	autoclawExecutorBaseURL = "http://127.0.0.1:1" // would fail if used

	rec := httptest.NewRecorder()
	err := ForwardAutoclaw(rec, &Request{
		Ctx:       t.Context(),
		Client:    srv.Client(),
		APIKey:    "rt-1|did-1|app-key-1",
		Body:      []byte(`{"model":"zai_auto","messages":[]}`),
		IsStream:  true,
		StartTime: time.Now(),
		Config:    &providers.ProviderConfig{BaseURL: srv.URL},
	})
	if err != nil {
		t.Fatalf("ForwardAutoclaw: %v", err)
	}
	if chatHits != 1 || refreshHits != 1 {
		t.Errorf("hits chat=%d refresh=%d, want 1/1 on the connection base URL", chatHits, refreshHits)
	}
}

func TestAutoclawPersistsRotatedRefreshToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/userapi/v1/refresh" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"Bearer at-rotated","refresh_token":"Bearer rt-rotated"}}`))
			return
		}
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"ok"}}]}` + "\n\n"))
	}))
	defer srv.Close()

	oldBase := autoclawExecutorBaseURL
	autoclawExecutorBaseURL = srv.URL
	defer func() { autoclawExecutorBaseURL = oldBase }()
	oldRefreshBase := oauth.SetAutoclawBaseURLForTest(srv.URL)
	defer func() { oauth.SetAutoclawBaseURLForTest(oldRefreshBase) }()

	var persistedAccess, persistedRefresh, persistedID string
	req := &Request{
		Ctx:          t.Context(),
		Client:       http.DefaultClient,
		Provider:     "autoclaw",
		APIKey:       "rt-rotation-unique|device-9|app-key-1",
		Body:         []byte(`{"model":"zai_auto","stream":false}`),
		ConnectionID: "autoclaw-conn-1",
		PersistCredentials: func(_ context.Context, id, access, refresh string) error {
			persistedID, persistedAccess, persistedRefresh = id, access, refresh
			return nil
		},
	}
	if err := ForwardAutoclaw(httptest.NewRecorder(), req); err != nil {
		t.Fatalf("ForwardAutoclaw: %v", err)
	}
	if persistedID != "autoclaw-conn-1" {
		t.Errorf("persisted connection id = %q", persistedID)
	}
	if persistedRefresh != "Bearer rt-rotated" {
		t.Errorf("persisted refresh token = %q, want rotated token", persistedRefresh)
	}
	if persistedAccess != "Bearer at-rotated" {
		t.Errorf("persisted access token = %q, want rotated token", persistedAccess)
	}
}
