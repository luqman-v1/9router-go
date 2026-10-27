package executor

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/proxy"
)

type customJSONUpstream struct {
	srv    *httptest.Server
	calls  atomic.Int32
	method string
	header http.Header
	body   map[string]any
}

func newCustomJSONUpstream(t *testing.T, status int, reply string) *customJSONUpstream {
	t.Helper()
	u := &customJSONUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.calls.Add(1)
		u.method = r.Method
		u.header = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &u.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *customJSONUpstream) request(t *testing.T, customJSON map[string]any, stream bool) *Request {
	t.Helper()
	return &Request{
		Ctx:      t.Context(),
		Client:   u.srv.Client(),
		Provider: "openai-compatible-custom-json-test",
		APIKey:   "test-secret",
		Body:     []byte(`{"model":"custom-model","messages":[{"role":"user","content":"hello"}],"stream":false}`),
		IsStream: stream,
		ConnData: map[string]any{
			"apiType":    "custom-json",
			"baseUrl":    u.srv.URL + "/api/run?version=2",
			"customJson": customJSON,
		},
	}
}

func TestForwardCustomJSON_SendsConfiguredRequestAndNormalizesReply(t *testing.T) {
	up := newCustomJSONUpstream(t, http.StatusOK, `{"data":{"answers":[{"text":"示例回复"}]}}`)
	rec := httptest.NewRecorder()
	err := ForwardCustomJSON(rec, up.request(t, map[string]any{
		"method": "PATCH", "authType": "api-key", "authHeader": "X-Service-Key",
		"headers":         map[string]any{"X-App": "9router"},
		"requestTemplate": map[string]any{"question": "{{prompt}}", "model": "{{model}}"},
		"responsePath":    "data.answers[0].text",
	}, false))
	if err != nil {
		t.Fatalf("ForwardCustomJSON: %v", err)
	}
	if up.method != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", up.method)
	}
	if up.header.Get("X-Service-Key") != "test-secret" || up.header.Get("X-App") != "9router" || up.header.Get("Authorization") != "" {
		t.Errorf("headers = %v", up.header)
	}
	if up.body["question"] != "hello" || up.body["model"] != "custom-model" {
		t.Errorf("upstream body = %v", up.body)
	}
	var completion struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &completion); err != nil {
		t.Fatalf("decode reply %q: %v", rec.Body.String(), err)
	}
	if completion.Object != "chat.completion" || completion.Choices[0].Message.Content != "示例回复" || completion.Choices[0].FinishReason != "stop" {
		t.Errorf("reply = %+v", completion)
	}
}

func TestForwardCustomJSON_StreamsCompleteSSEFraming(t *testing.T) {
	up := newCustomJSONUpstream(t, http.StatusOK, `{"answer":"hello\nworld"}`)
	rec := httptest.NewRecorder()
	if err := ForwardCustomJSON(rec, up.request(t, map[string]any{"responsePath": "answer"}, true)); err != nil {
		t.Fatalf("ForwardCustomJSON: %v", err)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q", ct)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"content":"hello\nworld"`) || !strings.Contains(out, `"finish_reason":"stop"`) || !strings.Contains(out, "data: [DONE]") {
		t.Errorf("stream = %q", out)
	}
	if up.body["stream"] != nil {
		// The default template sends model and messages only; stream is never
		// forwarded because the endpoint answers JSON.
		t.Errorf("upstream body carried stream: %v", up.body)
	}
}

func TestForwardCustomJSON_NoAuthSendsNoCredential(t *testing.T) {
	up := newCustomJSONUpstream(t, http.StatusOK, `{"answer":"OK"}`)
	req := up.request(t, map[string]any{"authType": "none", "responsePath": "answer"}, false)
	req.APIKey = ""
	if err := ForwardCustomJSON(httptest.NewRecorder(), req); err != nil {
		t.Fatalf("ForwardCustomJSON: %v", err)
	}
	if up.header.Get("Authorization") != "" {
		t.Errorf("Authorization = %q, want none", up.header.Get("Authorization"))
	}
}

func TestForwardCustomJSON_ErrorsAreNotReplayed(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			up := newCustomJSONUpstream(t, status, `{"error":"business error"}`)
			err := ForwardCustomJSON(httptest.NewRecorder(), up.request(t, map[string]any{}, false))
			var ue *proxy.UpstreamError
			if !errors.As(err, &ue) || ue.StatusCode != status {
				t.Fatalf("err = %v, want upstream %d", err, status)
			}
			if n := up.calls.Load(); n != 1 {
				t.Errorf("upstream called %d times, want 1", n)
			}
		})
	}
}

func TestForwardCustomJSON_UnmappableResponseIs502(t *testing.T) {
	for _, reply := range []string{`<html>not JSON</html>`, `{"unexpected":true}`} {
		up := newCustomJSONUpstream(t, http.StatusOK, reply)
		err := ForwardCustomJSON(httptest.NewRecorder(), up.request(t, map[string]any{}, false))
		var ue *proxy.UpstreamError
		if !errors.As(err, &ue) || ue.StatusCode != http.StatusBadGateway || !strings.Contains(string(ue.Body), "Custom JSON response:") {
			t.Errorf("reply %q: err = %v", reply, err)
		}
	}
}

func TestForwardCustomJSON_EmptyReplyIsServed(t *testing.T) {
	up := newCustomJSONUpstream(t, http.StatusOK, `{"answer":""}`)
	rec := httptest.NewRecorder()
	if err := ForwardCustomJSON(rec, up.request(t, map[string]any{"responsePath": "answer"}, false)); err != nil {
		t.Fatalf("empty business reply returned %v, want it served", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
}
