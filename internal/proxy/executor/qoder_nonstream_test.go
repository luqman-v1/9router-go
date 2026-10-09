package executor

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// qoderTestServer answers with the given raw SSE body, and accepts any path
// Qoder builds onto its configured endpoint.
func qoderTestServer(t *testing.T, sse string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sse))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// qoderRequest builds a request that satisfies the COSY signer: Qoder verifies
// the signature against the real account, so a request without a user id can
// never be sent. It also seeds the model catalogue, because chat resolves
// model_config from the live model list before it will send anything.
func qoderRequest(t *testing.T, srv *httptest.Server) *Request {
	t.Helper()
	creds := QoderCosyCreds{UserID: "user-1", AuthToken: "auth-token"}
	qoderSeedCatalog(t, creds, QoderRegionIntl, map[string]map[string]any{
		"model": {"key": "model", "max_input_tokens": 131072.0, "max_output_tokens": 8192.0},
	})
	return &Request{
		Ctx:      t.Context(),
		Client:   srv.Client(),
		Config:   &providers.ProviderConfig{BaseURL: srv.URL},
		APIKey:   creds.AuthToken,
		ConnData: map[string]any{"userId": creds.UserID},
		Body:     []byte(`{"model":"qd/model","messages":[{"role":"user","content":"hi"}],"stream":false}`),
		IsStream: false,
	}
}

// qoderSSE joins frames the way an event stream is written on the wire.
func qoderSSE(frames ...string) string {
	var b strings.Builder
	for _, f := range frames {
		b.WriteString("data: " + f + "\n\n")
	}
	return b.String()
}

func qoderChunk(delta, finish string) string {
	return fmt.Sprintf(
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"choices":[{"index":0,"delta":%s,"finish_reason":%s}]}`,
		delta, finish)
}

// Qoder's endpoint is SSE-only, so a `stream:false` request used to be
// answered with the raw event stream under an `application/json` header and
// the client's JSON.parse failed on `data: {...}` (issue #41).
func TestForwardQoder_NonStreamFoldsSSEIntoJSON(t *testing.T) {
	sse := qoderSSE(
		qoderChunk(`{"content":"Hey"}`, `null`),
		qoderChunk(`{"content":" there"}`, `null`),
		qoderChunk(`{}`, `"stop"`),
		"[DONE]",
	)
	srv := qoderTestServer(t, sse)
	rec := httptest.NewRecorder()
	if err := ForwardQoder(rec, qoderRequest(t, srv)); err != nil {
		t.Fatalf("ForwardQoder: %v", err)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var out struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body is not JSON, so a client JSON.parse fails: %v\nbody: %s", err, rec.Body.String())
	}
	if out.Object != "chat.completion" {
		t.Errorf("object = %q, want chat.completion", out.Object)
	}
	if len(out.Choices) != 1 {
		t.Fatalf("got %d choices, want 1", len(out.Choices))
	}
	if out.Choices[0].Message.Content != "Hey there" {
		t.Errorf("content = %q, want %q", out.Choices[0].Message.Content, "Hey there")
	}
}

// Qoder reports failures inside the event stream. Folding alone would turn a
// real upstream error into a successful empty completion, so the envelope has
// to surface as a status the fallback layer can act on (issue #41).
func TestForwardQoder_NonStreamSurfacesHiddenUpstreamError(t *testing.T) {
	inner := `{"code":"400","message":"[FAIL]node:agent_router flow nodes not found"}`
	frame, err := json.Marshal(map[string]any{
		"headers":         map[string]any{"Content-Type": []string{"application/json"}},
		"body":            inner,
		"statusCodeValue": 400,
		"statusCode":      "BAD_REQUEST",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := qoderTestServer(t, qoderSSE(string(frame), `{"firstTokenDuration":1,"totalDuration":2}`))

	rec := httptest.NewRecorder()
	err = ForwardQoder(rec, qoderRequest(t, srv))
	if err == nil {
		t.Fatalf("expected an error, got a 200 with body: %s", rec.Body.String())
	}
	var upErr *proxy.UpstreamError
	if !errors.As(err, &upErr) {
		t.Fatalf("error = %v, want a *proxy.UpstreamError", err)
	}
	if upErr.StatusCode != 400 {
		t.Errorf("StatusCode = %d, want 400", upErr.StatusCode)
	}

	if !strings.Contains(string(upErr.Body), "flow nodes not found") {
		t.Errorf("upstream message not readable by the client: %s", upErr.Body)
	}
	if strings.Contains(rec.Body.String(), "data:") {
		t.Errorf("SSE text must never reach the client: %s", rec.Body.String())
	}
}
