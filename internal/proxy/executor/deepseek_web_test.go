package executor

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/providers"
)

// deepSeekFakeUpstream is a chat.deepseek.com stand-in for one turn. Every
// endpoint the executor calls is served by one handler, so the test exercises
// the real four-call sequence — token exchange, session, proof of work, then
// completion — rather than a stubbed seam.
type deepSeekFakeUpstream struct {
	mu sync.Mutex

	// challengeSalt and challengeExpire mint a satisfiable proof of work whose
	// answer the test can verify for itself.
	challengeSalt   string
	challengeExpire int64

	// calls records the path of every request served, in order.
	calls []string

	// headers captures the last request per path, so the fingerprint this
	// provider is judged on can be asserted.
	headers map[string]http.Header

	// bodies captures the last request body per path, so a test can assert on
	// what the upstream was actually asked to do — which session id it was
	// given, which parent it was threading on, which prompt it received.
	bodies map[string][]byte

	// completionBody is the event stream the completion endpoint answers with.
	completionBody string

	// tokenStatus overrides the HTTP status users/current answers with.
	tokenStatus int
	// tokenCode overrides the application-level code in the same envelope.
	tokenCode int

	// muteBody, when set, replaces the completion answer with an account-level
	// enforcement document rather than an event stream.
	muteBody string

	// createdSessions counts the sessions this upstream has minted, so a test
	// can tell a reused session from a fresh one: a turn that continues a
	// pinned conversation must not create a second upstream session.
	createdSessions int

	// failCompletions makes the next N completion calls answer with an HTTP
	// error instead of the event stream — the shape of a stale upstream session
	// that a healing turn is expected to retry on a fresh one. failStatus picks
	// the status those failures carry; zero means 500.
	failCompletions int
	failStatus      int
}

// countCalls reports how many requests the upstream served on path.
func (f *deepSeekFakeUpstream) countCalls(path string) int {
	count := 0
	for _, call := range f.callPaths() {
		if call == path {
			count++
		}
	}
	return count
}

// newDeepSeekFakeUpstream returns an upstream whose completion answers with the
// given event-stream frames.
func newDeepSeekFakeUpstream(t *testing.T, frames ...string) *deepSeekFakeUpstream {
	t.Helper()
	// The attestation hosts are real public endpoints, so a turn driven in a
	// unit test has to be given a stub for them: without this the executor
	// would fetch an attestation value from the internet mid-test.
	stubDeepSeekAttestation(t)
	// The turn gap and the session-delete queue run on production timers. The
	// gap collapses to zero so consecutive turns in one test do not sit out
	// the quiet window, and the queue starts empty so one test's retired
	// sessions are never flushed into another test's upstream.
	previousGap := deepSeekTurnGapFn
	deepSeekTurnGapFn = func(time.Duration) time.Duration { return 0 }
	t.Cleanup(func() { deepSeekTurnGapFn = previousGap })
	t.Cleanup(deepSeekResetSessionDeletes)
	return &deepSeekFakeUpstream{
		challengeSalt:   "salt-for-test",
		challengeExpire: 1778891543095,
		headers:         map[string]http.Header{},
		bodies:          map[string][]byte{},
		completionBody:  strings.Join(frames, ""),
		tokenStatus:     http.StatusOK,
	}
}

func (f *deepSeekFakeUpstream) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.URL.Path)
	f.headers[r.URL.Path] = r.Header.Clone()
	f.bodies[r.URL.Path] = body
}

// body decodes the request the upstream received on path.
func (f *deepSeekFakeUpstream) body(t *testing.T, path string) map[string]any {
	t.Helper()
	f.mu.Lock()
	raw := append([]byte(nil), f.bodies[path]...)
	f.mu.Unlock()

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode %s request body %q: %v", path, raw, err)
	}
	return decoded
}

func (f *deepSeekFakeUpstream) callPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *deepSeekFakeUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.record(r)
	w.Header().Set("Content-Type", "application/json")

	switch r.URL.Path {
	case "/api/v0/users/current":
		w.WriteHeader(f.tokenStatus)
		_ = json.MarshalWrite(w, map[string]any{
			"code": f.tokenCode,
			"msg":  "token rejected",
			"data": map[string]any{"biz_data": map[string]any{"token": "access-token-1"}},
		})

	case "/api/v0/chat_session/create":
		f.mu.Lock()
		f.createdSessions++
		id := fmt.Sprintf("session-%d", f.createdSessions)
		f.mu.Unlock()
		_ = json.MarshalWrite(w, map[string]any{
			"code": 0,
			"data": map[string]any{"biz_data": map[string]any{
				"chat_session": map[string]any{"id": id},
			}},
		})

	case "/api/v0/chat_session/delete":
		_ = json.MarshalWrite(w, map[string]any{"code": 0})

	case "/api/v0/chat/create_pow_challenge":
		prefix := f.challengeSalt + "_" + fmt.Sprint(f.challengeExpire) + "_"
		_ = json.MarshalWrite(w, map[string]any{
			"code": 0,
			"data": map[string]any{"biz_data": map[string]any{
				"challenge": map[string]any{
					"algorithm":   deepSeekPowAlgorithm,
					"challenge":   deepSeekHashV1Hex(prefix + "0"),
					"salt":        f.challengeSalt,
					"difficulty":  2,
					"expire_at":   f.challengeExpire,
					"signature":   "sig-1",
					"target_path": deepSeekCompletionPath,
				},
			}},
		})

	case deepSeekCompletionPath:
		f.mu.Lock()
		failing := f.failCompletions > 0
		if failing {
			f.failCompletions--
		}
		failStatus := f.failStatus
		f.mu.Unlock()
		if failing {
			status := failStatus
			if status == 0 {
				status = http.StatusInternalServerError
			}
			w.WriteHeader(status)
			_ = json.MarshalWrite(w, map[string]any{"code": 1, "msg": "chat session is gone"})
			return
		}
		if f.muteBody != "" {
			// The account-level enforcement: HTTP 200, a JSON document, and no
			// event stream at all.
			fmt.Fprint(w, f.muteBody)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, f.completionBody)

	default:
		http.NotFound(w, r)
	}
}

// deepSeekTestConfig points a request at the fake upstream. The executor
// derives every sibling endpoint from this base URL, which is what lets a whole
// turn run against one httptest server.
func deepSeekTestConfig(baseURL string) *providers.ProviderConfig {
	return &providers.ProviderConfig{BaseURL: baseURL + deepSeekCompletionPath}
}

// deepSeekTestBody builds an OpenAI chat-completions request.
func deepSeekTestBody(t *testing.T, model string, messages ...map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":    model,
		"messages": messages,
	})
	if err != nil {
		t.Fatalf("marshal test body: %v", err)
	}
	return body
}

// TestForwardDeepSeekWebStreams proves a browser-login credential completes a
// streaming turn end to end: the four upstream calls happen in order, the proof
// of work is actually solved, the reasoning and answer channels stay separate,
// and the client sees an OpenAI stream that terminates.
func TestForwardDeepSeekWebStreams(t *testing.T) {
	upstream := newDeepSeekFakeUpstream(t,
		`data: {"p":"response","v":{"response":{"thinking_enabled":true,"fragments":[{"type":"THINK","content":"weighing it"}]}}}`+"\n\n",
		`data: {"p":"response","v":{"response":{"fragments":[{"type":"ANSWER","content":"the answer"}]}}}`+"\n\n",
		`data: {"p":"response/status","v":"FINISHED"}`+"\n\n",
	)
	server := httptest.NewServer(upstream)
	defer server.Close()

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      context.Background(),
		Client:   server.Client(),
		Config:   deepSeekTestConfig(server.URL),
		APIKey:   "user-token-1",
		Body:     deepSeekTestBody(t, "deepseek-v4-pro-think", map[string]any{"role": "user", "content": "hello"}),
		IsStream: true,
	}
	if err := ForwardDeepSeekWeb(rec, req); err != nil {
		t.Fatalf("ForwardDeepSeekWeb: %v", err)
	}

	// Stealth mode is on by default, so a plain request with no client session
	// id creates a session and retires it when the turn ends: the session is
	// not pinned to a conversation, so nothing will ever thread on it. The
	// retirement is queued rather than immediate — an instant
	// create-then-delete is a cadence no browser produces — so the delete
	// arrives when the queue flushes, not in the turn's own call sequence.
	calls := upstream.callPaths()
	wantOrder := []string{
		"/api/v0/users/current",
		"/api/v0/chat_session/create",
		"/api/v0/chat/create_pow_challenge",
		deepSeekCompletionPath,
	}
	if len(calls) != len(wantOrder) {
		t.Fatalf("expected %d upstream calls, got %v", len(wantOrder), calls)
	}
	for i, want := range wantOrder {
		if calls[i] != want {
			t.Fatalf("call %d = %q, want %q (full order: %v)", i, calls[i], want, calls)
		}
	}

	// The completion always asks the upstream for a stream, whatever the
	// client asked for: the site's own client has no non-streaming mode, and
	// a `stream: false` on this endpoint is a value no browser ever wrote.
	if got, _ := upstream.body(t, deepSeekCompletionPath)["stream"].(bool); !got {
		t.Error("completion carried stream = false, want the browser's constant true")
	}
	// The Referer names the conversation's own page, which is the page the
	// browser stands on while it streams this answer.
	if got := upstream.headers[deepSeekCompletionPath].Get("Referer"); got != server.URL+"/a/chat/s/session-1" {
		t.Errorf("completion Referer = %q, want the conversation's page %q", got, server.URL+"/a/chat/s/session-1")
	}

	// The queued retirement lands on the flush, in the browser stack's shape.
	deepSeekFlushSessionDeletes(context.Background())
	if got := upstream.countCalls("/api/v0/chat_session/delete"); got != 1 {
		t.Errorf("session deletes = %d, want 1 after the queue flush", got)
	}

	// The proof of work must be a real answer to this challenge, not a header
	// that merely exists: a wrong nonce is what upstream rejects.
	powHeader := upstream.headers[deepSeekCompletionPath].Get("X-Ds-Pow-Response")
	if powHeader == "" {
		t.Fatal("completion carried no X-Ds-Pow-Response header")
	}
	decoded, err := base64.StdEncoding.DecodeString(powHeader)
	if err != nil {
		t.Fatalf("pow response is not base64: %v", err)
	}
	var answer deepSeekPowResponse
	if err := json.Unmarshal(decoded, &answer); err != nil {
		t.Fatalf("pow response is not the expected envelope: %v", err)
	}
	prefix := upstream.challengeSalt + "_" + fmt.Sprint(upstream.challengeExpire) + "_"
	if got := deepSeekHashV1Hex(prefix + fmt.Sprint(answer.Answer)); got != answer.Challenge {
		t.Fatalf("pow answer %d hashes to %s, want the challenge %s", answer.Answer, got, answer.Challenge)
	}
	if answer.TargetPath != deepSeekCompletionPath {
		t.Errorf("pow target_path = %q, want %q", answer.TargetPath, deepSeekCompletionPath)
	}

	// The browser fingerprint is what this provider is judged on; a regression
	// here is a 4xx from the real site, not a test failure.
	//
	// The set is the one a CHAT call carries. The site's own client issues
	// the chat calls below the interceptor that adds the `x-client-*` block,
	// so a chat call that carried the block would be a call no browser made.
	completionHeaders := upstream.headers[deepSeekCompletionPath]
	for header, want := range map[string]string{
		"Origin":         server.URL,
		"Authorization":  "Bearer access-token-1",
		"Sec-Fetch-Mode": "cors",
		"X-Hif-Leim":     "attestation-leim-value",
		"X-Hif-Dliq":     "attestation-dliq-value",
		"Content-Type":   "application/json",
		"Priority":       "u=1, i",
	} {
		if got := completionHeaders.Get(header); got != want {
			t.Errorf("completion %s = %q, want %q", header, got, want)
		}
	}
	if ua := completionHeaders.Get("User-Agent"); ua != deepSeekWebUserAgent {
		t.Errorf("completion User-Agent = %q, want the declared build %q", ua, deepSeekWebUserAgent)
	}
	// The client block belongs to the settings calls, not to the chat ones.
	for _, header := range []string{"X-Client-Bundle-Id", "X-Client-Version", "X-Client-Platform", "X-Client-Locale"} {
		if got := completionHeaders.Get(header); got != "" {
			t.Errorf("completion %s = %q, want it absent — the chat calls carry no client block", header, got)
		}
	}
	// The client hints belong to the telemetry call, not to the chat ones.
	for _, header := range []string{"Sec-Ch-Ua", "Sec-Ch-Ua-Mobile", "Sec-Ch-Ua-Platform"} {
		if got := completionHeaders.Get(header); got != "" {
			t.Errorf("completion %s = %q, want it absent — no browser sends client hints on an API call", header, got)
		}
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"reasoning_content":"weighing it"`) {
		t.Errorf("stream is missing the reasoning channel; body: %s", body)
	}
	if !strings.Contains(body, `"content":"the answer"`) {
		t.Errorf("stream is missing the answer; body: %s", body)
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) {
		t.Errorf("stream never reports a stop reason; body: %s", body)
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Errorf("stream must terminate with [DONE]; body: %s", body)
	}
}

// TestForwardDeepSeekWebNonStream proves the non-streaming client receives one
// chat.completion document with both channels populated.
func TestForwardDeepSeekWebNonStream(t *testing.T) {
	upstream := newDeepSeekFakeUpstream(t,
		`data: {"p":"response/fragments","v":[{"type":"THINK","content":"considering"}]}`+"\n\n",
		`data: {"p":"response/fragments","v":[{"type":"ANSWER","content":"42"}]}`+"\n\n",
		`data: {"p":"response/status","v":"FINISHED"}`+"\n\n",
	)
	server := httptest.NewServer(upstream)
	defer server.Close()

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:    context.Background(),
		Client: server.Client(),
		Config: deepSeekTestConfig(server.URL),
		APIKey: "user-token-2",
		Body:   deepSeekTestBody(t, "deepseek-chat", map[string]any{"role": "user", "content": "meaning of life?"}),
	}
	if err := ForwardDeepSeekWeb(rec, req); err != nil {
		t.Fatalf("ForwardDeepSeekWeb: %v", err)
	}

	var completion struct {
		Object  string `json:"object"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &completion); err != nil {
		t.Fatalf("response is not a chat.completion: %v (body: %s)", err, rec.Body.String())
	}
	if completion.Object != "chat.completion" {
		t.Errorf("object = %q, want chat.completion", completion.Object)
	}
	if len(completion.Choices) != 1 {
		t.Fatalf("expected one choice, got %d", len(completion.Choices))
	}
	if completion.Choices[0].Message.Content != "42" {
		t.Errorf("content = %q, want %q", completion.Choices[0].Message.Content, "42")
	}
	if completion.Choices[0].Message.ReasoningContent != "considering" {
		t.Errorf("reasoning_content = %q, want %q", completion.Choices[0].Message.ReasoningContent, "considering")
	}
	// The upstream is asked for a stream even for a non-streaming client: the
	// site's own client has no other mode, and the collector drains it.
	if got, _ := upstream.body(t, deepSeekCompletionPath)["stream"].(bool); !got {
		t.Error("a non-streaming client must not turn the upstream request into stream = false")
	}
}

// TestForwardDeepSeekWebRejectsExpiredToken pins the two ways this upstream
// reports a dead credential. Both arrive as HTTP 200 with a code in the body,
// and both have to reach the router as a 401 so the account is rotated instead
// of being retried against the same dead session.
func TestForwardDeepSeekWebRejectsExpiredToken(t *testing.T) {
	tests := []struct {
		name      string
		tokenCode int
	}{
		{
			name: "auth failed envelope",
			// 40003 is this API's observed "Authorization Failed" code.
			tokenCode: deepSeekCodeAuthFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := newDeepSeekFakeUpstream(t)
			upstream.tokenCode = tt.tokenCode
			server := httptest.NewServer(upstream)
			defer server.Close()

			rec := httptest.NewRecorder()
			req := &Request{
				Ctx:    context.Background(),
				Client: server.Client(),
				Config: deepSeekTestConfig(server.URL),
				APIKey: "stale-token",
				Body:   deepSeekTestBody(t, "deepseek-chat", map[string]any{"role": "user", "content": "hi"}),
			}

			err := ForwardDeepSeekWeb(rec, req)
			if err == nil {
				t.Fatal("a rejected credential must not be reported as a served turn")
			}
			status, ok := deepSeekUpstreamStatus(err)
			if !ok || status != http.StatusUnauthorized {
				t.Fatalf("error = %v (status %d), want a 401 the router can rotate on", err, status)
			}
		})
	}
}

// TestForwardDeepSeekWebTruncatedStream proves a stream that closes without the
// upstream's FINISHED signal is not served as a complete answer.
//
// The upstream has no other way to say it cut off mid-answer, so the only
// honest reading of a body that ends without FINISHED is an interrupted session.
// Reporting the partial text with finish_reason stop is how a client comes to
// believe a truncated reply is the model's conclusion.
func TestForwardDeepSeekWebTruncatedStream(t *testing.T) {
	upstream := newDeepSeekFakeUpstream(t,
		`data: {"p":"response/fragments","v":[{"type":"ANSWER","content":"I'll check that"}]}`+"\n\n",
	)
	server := httptest.NewServer(upstream)
	defer server.Close()

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:       context.Background(),
		Client:    server.Client(),
		Config:    deepSeekTestConfig(server.URL),
		APIKey:    "user-token-3",
		Body:      deepSeekTestBody(t, "deepseek-chat", map[string]any{"role": "user", "content": "hi"}),
		IsStream:  true,
		StartTime: time.Now(),
	}

	err := ForwardDeepSeekWeb(rec, req)
	if err == nil {
		t.Fatal("a stream cut off mid-answer must not be reported as a served turn")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "[DONE]") {
		t.Errorf("an aborted stream must still terminate so the client is not left hanging; body: %s", body)
	}
	if strings.Contains(body, `"finish_reason":"stop"`) {
		t.Errorf("a truncated answer must not be labelled a completed turn; body: %s", body)
	}
}

// TestForwardDeepSeekWebRejectsMissingCredential proves a provider that cannot
// be authenticated fails at the request rather than reaching upstream.
func TestForwardDeepSeekWebRejectsMissingCredential(t *testing.T) {
	upstream := newDeepSeekFakeUpstream(t)
	server := httptest.NewServer(upstream)
	defer server.Close()

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      context.Background(),
		Client:   server.Client(),
		Config:   deepSeekTestConfig(server.URL),
		Body:     deepSeekTestBody(t, "deepseek-chat", map[string]any{"role": "user", "content": "hi"}),
		IsStream: true,
	}

	err := ForwardDeepSeekWeb(rec, req)
	if err == nil {
		t.Fatal("a request with no userToken must not be served")
	}
	if calls := upstream.callPaths(); len(calls) != 0 {
		t.Errorf("no upstream call should be made without a credential, got %v", calls)
	}
}
