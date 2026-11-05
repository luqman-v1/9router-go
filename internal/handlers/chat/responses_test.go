package chat

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/translator"
)

const responsesRequestBody = `{"model":"%s","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true}`

// upstreamCapture records what a provider actually received so a test can
// assert the shape of the request, not just the shape of the reply.
type upstreamCapture struct {
	mu   sync.Mutex
	body string
}

func (c *upstreamCapture) got() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.body
}

func (c *upstreamCapture) record(b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.body = string(b)
}

// TestHandleResponses_ChatUpstreamIsBridged is the end-to-end contract for a
// /v1/responses client talking to a Chat Completions provider.
func TestHandleResponses_ChatUpstreamIsBridged(t *testing.T) {
	capture := &upstreamCapture{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		capture.record(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	seedConnDB(t, database, "bn", "conn-responses-bridge", "sk-bridge", upstream.URL)
	handler := NewChatHandler(db.NewRepo(database))

	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader([]byte(fmt.Sprintf(responsesRequestBody, "bn/claude-sonnet-4.5"))))
	rec := httptest.NewRecorder()
	handler.HandleResponses(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	sent := capture.got()
	if !strings.Contains(sent, `"messages"`) {
		t.Errorf("upstream never received a Chat Completions body: %s", sent)
	}
	if strings.Contains(sent, `"input"`) {
		t.Errorf("Responses input[] leaked to a chat upstream: %s", sent)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "event: response.output_text.delta") || !strings.Contains(out, "pong") {
		t.Errorf("client did not receive Responses text events:\n%s", out)
	}
	if !strings.Contains(out, "event: response.completed") {
		t.Errorf("stream was never closed with response.completed:\n%s", out)
	}
}

// TestHandleResponses_NativeUpstreamIsNotTranslated is the guard on the other
// side: a Responses-native endpoint must keep the request it was given, because
// converting it to Chat Completions silently drops previous_response_id and
// item ids, and codex loses its server-side conversation.
func TestHandleResponses_NativeUpstreamIsNotTranslated(t *testing.T) {
	capture := &upstreamCapture{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		capture.record(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"pong\"}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"))

	}))
	defer upstream.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	seedConnDB(t, database, "codex", "conn-responses-native", "sk-codex", upstream.URL+"/responses")
	handler := NewChatHandler(db.NewRepo(database))

	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader([]byte(fmt.Sprintf(responsesRequestBody, "codex/gpt-5.1"))))
	rec := httptest.NewRecorder()
	handler.HandleResponses(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	sent := capture.got()
	if !strings.Contains(sent, `"input"`) {
		t.Errorf("native upstream lost its input[] payload: %s", sent)
	}
	if strings.Contains(sent, `"messages"`) {
		t.Errorf("request was translated for a Responses-native upstream: %s", sent)
	}
	if !strings.Contains(rec.Body.String(), "response.output_text.delta") {
		t.Errorf("native events were not relayed:\n%s", rec.Body.String())
	}
}

// TestHandleResponses_NonStreamingAnswersWithResponseObject is the same contract
// for a client that did not ask to stream: one Response object, not a Chat
// Completions body the client cannot read.
func TestHandleResponses_NonStreamingAnswersWithResponseObject(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","model":"fake","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":4}}`))
	}))
	defer upstream.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	seedConnDB(t, database, "bn", "conn-responses-json", "sk-json", upstream.URL)
	handler := NewChatHandler(db.NewRepo(database))

	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader([]byte(`{"model":"bn/fake-model","input":"ping"}`)))
	rec := httptest.NewRecorder()
	handler.HandleResponses(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	if strings.Contains(out, "chat.completion") {
		t.Errorf("Chat Completions body reached a Responses client:\n%s", out)
	}
	for _, want := range []string{`"object":"response"`, `"status":"completed"`, "pong"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in the response body:\n%s", want, out)
		}
	}
}

// TestHandleResponsesCompact_MarksRequestAndKeepsWireFormat covers the whole
// compact chain: the endpoint marks the body, the codex executor turns that
// into a /compact URL and strips the marker, and the client still gets
// Responses — not a Chat Completions body, which is what routing the request
// through /v1/chat/completions used to produce.
func TestHandleResponsesCompact_MarksRequestAndKeepsWireFormat(t *testing.T) {
	capture := &upstreamCapture{}
	path := ""
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		capture.record(raw)
		path = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"))
	}))
	defer upstream.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	seedConnDB(t, database, "codex", "conn-responses-compact", "sk-codex", upstream.URL+"/responses")
	handler := NewChatHandler(db.NewRepo(database))

	req := httptest.NewRequest("POST", "/v1/responses/compact", bytes.NewReader([]byte(`{"model":"codex/gpt-5.1","input":"compact me","stream":true}`)))
	rec := httptest.NewRecorder()
	handler.HandleResponsesCompact(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if path != "/responses/compact" {
		t.Errorf("compaction did not reach the /compact endpoint, path = %q", path)
	}
	if sent := capture.got(); strings.Contains(sent, "_compact") {
		t.Errorf("the compact marker leaked upstream: %s", sent)
	}
	if out := rec.Body.String(); strings.Contains(out, "chat.completion") {
		t.Errorf("compact request answered in Chat Completions format:\n%s", out)
	}
}

// TestHandleResponses_ConnectionProblemsAreReported covers the two setup
// mistakes a client can make, through the same endpoint. The status codes
// follow /v1/chat/completions, not the old media passthrough: an unreachable
// provider is a bad gateway here, and a connection without a usable key is an
// auth failure, so a client sees one consistent story across the two wire
// formats it can speak.
func TestHandleResponses_ConnectionProblemsAreReported(t *testing.T) {
	t.Run("provider has no connection at all", func(t *testing.T) {
		database, cleanup := setupChatTestDB(t)
		defer cleanup()
		handler := NewChatHandler(db.NewRepo(database))

		req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader([]byte(`{"model":"nonexistent/foo","input":"hi"}`)))
		rec := httptest.NewRecorder()
		handler.HandleResponses(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Errorf("expected 502, got %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "no active connections") {
			t.Errorf("error should name the missing connection: %s", rec.Body.String())
		}
	})

	t.Run("connection carries no usable key", func(t *testing.T) {
		// The upstream is local and counts its hits: a request that reached it
		// would mean the gateway dispatched a call it had no credentials for,
		// and pointing this at a real provider would make the unit test depend
		// on the network.
		var hits int
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			w.WriteHeader(http.StatusOK)
		}))
		defer upstream.Close()

		database, cleanup := setupChatTestDB(t)
		defer cleanup()
		seedConnDB(t, database, "bn", "conn-responses-nokey", "", upstream.URL)
		handler := NewChatHandler(db.NewRepo(database))

		req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader([]byte(`{"model":"bn/fake-model","input":"hi"}`)))
		rec := httptest.NewRecorder()
		handler.HandleResponses(rec, req)

		// The connection is unusable, so the account fallback exhausts its
		// candidates and reports the provider as unavailable — the same thing
		// /v1/chat/completions does. What matters for a Responses client is
		// that it is refused and that nothing was billed upstream.
		if rec.Code == http.StatusOK {
			t.Errorf("a connection without credentials must not answer 200: %s", rec.Body.String())
		}
		if hits != 0 {
			t.Errorf("upstream was called %d times without credentials", hits)
		}
	})
}

// TestHandleResponses_ComboRotatesAndStaysInWireFormat proves the converted
// body survives account rotation: a combo whose first member is down must
// still answer the client in Responses, not in the Chat Completions shape the
// failing member would have produced.
func TestHandleResponses_ComboRotatesAndStaysInWireFormat(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer down.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","model":"fake","choices":[{"index":0,"message":{"role":"assistant","content":"from second"},"finish_reason":"stop"}]}`))
	}))
	defer up.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	seedConnDB(t, database, "bn", "conn-combo-down", "sk-1", down.URL)
	seedConnDB(t, database, "bnalt", "conn-combo-up", "sk-2", up.URL)
	models, _ := json.Marshal([]string{"bn/first", "bnalt/second"})
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt)
		VALUES ('combo-resp', 'responses-combo', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(models)); err != nil {
		t.Fatalf("seed combo: %v", err)
	}
	handler := NewChatHandler(db.NewRepo(database))

	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader([]byte(`{"model":"responses-combo","input":"hi"}`)))
	rec := httptest.NewRecorder()
	handler.HandleResponses(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	if !strings.Contains(out, "from second") {
		t.Errorf("combo did not fall back to the healthy member: %s", out)
	}
	if strings.Contains(out, "chat.completion") {
		t.Errorf("combo answer left the Responses wire format: %s", out)
	}
}

// TestHandleResponses_TokenSaverBypass verifies that the X-9Router-Token-Saver
// header opts a /v1/responses request out of every token saver, matching the
// /v1/chat/completions and /v1/messages paths. With the header set the body
// reaches the upstream untouched; without it RTK compresses repetitive output.
func TestHandleResponses_TokenSaverBypass(t *testing.T) {
	// Repetitive tool output that RTK compresses when saving is on.
	repeated := strings.Repeat("repeated log line\n", 300)
	payload := map[string]any{
		"model": "codex/gpt-5.1",
		"input": []any{
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": repeated},
		},
		"stream": true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	// The JSON-escaped form of the repetitive output as it appears in the wire body.
	outputJSON, _ := json.Marshal(repeated)

	newHandler := func(t *testing.T) (*ChatHandler, *httptest.Server, *[]byte) {
		var received []byte
		var mu sync.Mutex
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			mu.Lock()
			received = append(received[:0], raw...)
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"))
		}))
		database, cleanup := setupChatTestDB(t)
		t.Cleanup(cleanup)
		seedConnDB(t, database, "codex", "conn-responses-bypass", "sk-codex", upstream.URL+"/responses")
		handler := NewChatHandler(db.NewRepo(database))
		handler.TokenSaver.SetRTK(true)
		return handler, upstream, &received
	}

	assertBodyUntouched := func(t *testing.T, header string) {
		t.Helper()
		handler, upstream, received := newHandler(t)
		defer upstream.Close()

		req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
		if header != "" {
			req.Header.Set("X-9Router-Token-Saver", header)
		}
		rec := httptest.NewRecorder()
		handler.HandleResponses(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if !bytes.Contains(*received, outputJSON) {
			t.Errorf("expected body untouched (header=%q); RTK compressed the output:\n%s", header, string(*received))
		}
	}

	t.Run("bypass header leaves body untouched", func(t *testing.T) {
		assertBodyUntouched(t, "off")
	})

	t.Run("all bypass values opt out", func(t *testing.T) {
		bypassValues := []string{"off", "false", "0", "bypass", "OFF", "False", " 0 "}
		for _, val := range bypassValues {
			t.Run(val, func(t *testing.T) {
				assertBodyUntouched(t, val)
			})
		}
	})

	t.Run("without header RTK compresses", func(t *testing.T) {
		handler, upstream, received := newHandler(t)
		defer upstream.Close()

		req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		handler.HandleResponses(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if bytes.Contains(*received, outputJSON) {
			t.Errorf("without the bypass header RTK should compress the repetitive output:\n%s", string(*received))
		}
	})
}

// TestHandleGeminiStream_ResponsesClient covers the antigravity hop: Gemini
// events become OpenAI chunks and those become Responses events. Without the
// bridge this path wrote the intermediate Chat chunks straight to the client,
// which a /v1/responses client cannot read.
func TestHandleGeminiStream_ResponsesClient(t *testing.T) {
	upstream := strings.Join([]string{
		// antigravity wraps each streamed chunk in a {"response": ...} envelope.
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Hel"}]}}]}}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]},"finishReason":"STOP"}]}}`,
		"",
	}, "\n\n")

	rec := httptest.NewRecorder()
	ctx := translator.WithResponsesBridge(translator.WithClientFormat(context.Background(), translator.ClientFormatResponses))
	metrics := &streamMetrics{}

	if err := (&ChatHandler{}).handleGeminiStream(ctx, rec, strings.NewReader(upstream), false, metrics); err != nil {
		t.Fatalf("handleGeminiStream: %v", err)
	}

	out := rec.Body.String()
	if strings.Contains(out, "chat.completion.chunk") {
		t.Errorf("Chat chunks reached a Responses client:\n%s", out)
	}
	if !strings.Contains(out, "event: response.completed") {
		t.Errorf("gemini hop never closed with response.completed:\n%s", out)
	}
	if !strings.Contains(out, `"text":"Hello"`) {
		t.Errorf("answer text did not survive the two hops:\n%s", out)
	}
}
