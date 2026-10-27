package executor

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// Perplexity's stream is not OpenAI's: content arrives in markdown blocks whose
// chunks accumulate, and the same block is re-sent on every event. The reader
// has to emit only the new tail or the answer repeats itself.
func TestReadPplxEventsEmitsOnlyTheNewTail(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"backend_uuid":"b-1","blocks":[{"intended_usage":"markdown_block","markdown_block":{"progress":"PENDING","chunks":["Hello"]}}]}`,
		``,
		`data: {"blocks":[{"intended_usage":"markdown_block","markdown_block":{"progress":"PENDING","chunks":[" world"]}}]}`,
		``,
		`event: end_of_stream`,
		``,
	}, "\n")

	var deltas []string
	var answer string
	readPplxEvents(strings.NewReader(stream), func(ev pplxEvent) bool {
		if ev.delta != "" {
			deltas = append(deltas, ev.delta)
		}
		if ev.answer != "" {
			answer = ev.answer
		}
		return true
	})

	if got := strings.Join(deltas, ""); got != "Hello world" {
		t.Errorf("deltas = %q, want %q", got, "Hello world")
	}
	if answer != "Hello world" {
		t.Errorf("answer = %q, want %q", answer, "Hello world")
	}
}

// The search steps are surfaced as reasoning, and a repeated step is not
// emitted twice.
func TestReadPplxEventsSurfacesSearchStepsOnce(t *testing.T) {
	step := `{"blocks":[{"intended_usage":"pro_search_steps","plan_block":{"steps":[{"step_type":"SEARCH_WEB","search_web_content":{"queries":[{"query":"golang proxy"}]}}]}}]}`
	stream := "data: " + step + "\n\ndata: " + step + "\n\nevent: end_of_stream\n\n"

	var thinking []string
	readPplxEvents(strings.NewReader(stream), func(ev pplxEvent) bool {
		if ev.thinking != "" {
			thinking = append(thinking, ev.thinking)
		}
		return true
	})

	if len(thinking) != 1 {
		t.Fatalf("thinking events = %d (%v), want 1", len(thinking), thinking)
	}
	if !strings.Contains(thinking[0], "golang proxy") {
		t.Errorf("thinking = %q, want it to name the query", thinking[0])
	}
}

func TestReadPplxEventsReportsAnUpstreamError(t *testing.T) {
	stream := `data: {"error_code":"AUTH","error_message":"session expired"}` + "\n\n"
	var gotErr string
	readPplxEvents(strings.NewReader(stream), func(ev pplxEvent) bool {
		if ev.err != "" {
			gotErr = ev.err
		}
		return true
	})
	if gotErr != "session expired" {
		t.Errorf("err = %q, want %q", gotErr, "session expired")
	}
}

// Citation markers, grok tags and response wrappers are Perplexity artefacts,
// not answer text.
func TestCleanPplxResponse(t *testing.T) {
	in := "<?xml version=\"1.0\"?><response>Go is fast [1]. <grok:tool>x</grok:tool>It <grok:y/> scales [12].</response>"
	got := cleanPplxResponse(in, true)
	if strings.Contains(got, "[1]") || strings.Contains(got, "grok:") || strings.Contains(got, "<response") {
		t.Errorf("artefacts survived: %q", got)
	}
	if !strings.Contains(got, "Go is fast") || !strings.Contains(got, "It") {
		t.Errorf("answer text was lost: %q", got)
	}
}

// The conversation is split the way the envelope needs: system prompt aside,
// history before, and the last user turn as the question.
func TestParsePplxMessages(t *testing.T) {
	raw := []any{
		map[string]any{"role": "system", "content": "be terse"},
		map[string]any{"role": "user", "content": "first"},
		map[string]any{"role": "assistant", "content": "answer one"},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "second"},
		}},
	}
	got := parsePplxMessages(raw)
	if !strings.Contains(got.system, "be terse") {
		t.Errorf("system = %q", got.system)
	}
	if len(got.history) != 2 {
		t.Fatalf("history = %d, want 2", len(got.history))
	}
	if got.current != "second" {
		t.Errorf("current = %q, want second", got.current)
	}
}

// A follow-up continues the same backend thread, so only the question travels —
// resending the whole history would fork the conversation.
func TestBuildPplxQueryOnFollowUpIsJustTheQuestion(t *testing.T) {
	p := pplxParsed{system: "sys", current: "what next", history: []pplxMessage{{Role: "user", Content: "x"}}}
	if got := buildPplxQuery(p, "backend-uuid", nil); got != "what next" {
		t.Errorf("query = %q, want just the question", got)
	}
}

func TestBuildPplxEnvelopeShape(t *testing.T) {
	env := buildPplxEnvelope("q", "copilot", "gpt54", "")
	params, ok := env["params"].(map[string]any)
	if !ok {
		t.Fatalf("params is %T", env["params"])
	}
	if params["mode"] != "copilot" || params["model_preference"] != "gpt54" {
		t.Errorf("mode/pref = %v/%v", params["mode"], params["model_preference"])
	}
	if params["version"] != perplexityWebAPIVersion {
		t.Errorf("version = %v", params["version"])
	}
	// A first turn has no backend conversation to continue.
	if params["last_backend_uuid"] != nil {
		t.Errorf("last_backend_uuid = %v, want nil on a first turn", params["last_backend_uuid"])
	}
	// The ids are generated, not empty.
	if params["frontend_uuid"] == "" || params["frontend_uuid"] == nil {
		t.Error("frontend_uuid is empty")
	}
}

// The whole lane, end to end: a Chat request in, an OpenAI answer out.
func TestForwardPerplexityWebNonStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Cookie"), "__Secure-next-auth.session-token") {
			t.Errorf("no session cookie sent: %q", r.Header.Get("Cookie"))
		}
		if r.Header.Get("X-App-ApiClient") != "default" {
			t.Errorf("X-App-ApiClient = %q", r.Header.Get("X-App-ApiClient"))
		}
		var body map[string]any
		if err := json.UnmarshalRead(r.Body, &body); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		if _, ok := body["params"]; !ok {
			t.Error("request is not the Perplexity envelope")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"backend_uuid":"b1","blocks":[{"intended_usage":"markdown_block","markdown_block":{"progress":"DONE","chunks":["Answer [1] here."]}}]}` + "\n\n"))
		w.Write([]byte("event: end_of_stream\n\n"))
	}))
	defer upstream.Close()

	req := &Request{
		Body:      []byte(`{"model":"pplx-auto","messages":[{"role":"user","content":"hi"}]}`),
		ModelName: "pplx-auto",
		Config:    pplxTestConfig(upstream.URL),
		APIKey:    "cookie-value",
		ConnData:  map[string]any{},
	}
	rec := httptest.NewRecorder()
	if err := ForwardPerplexityWeb(rec, req); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse: %v", err)
	}
	choices, _ := out["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices = %d", len(choices))
	}
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	content, _ := msg["content"].(string)
	if !strings.Contains(content, "Answer") {
		t.Errorf("content = %q", content)
	}
	// Citations are stripped on the way out.
	if strings.Contains(content, "[1]") {
		t.Errorf("citation marker survived: %q", content)
	}
}

// The streamed form emits OpenAI chunks that concatenate to the same answer.
func TestForwardPerplexityWebStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"blocks":[{"intended_usage":"markdown_block","markdown_block":{"progress":"PENDING","chunks":["Hel"]}}]}` + "\n\n"))
		w.Write([]byte(`data: {"blocks":[{"intended_usage":"markdown_block","markdown_block":{"progress":"PENDING","chunks":["lo"]}}]}` + "\n\n"))
		w.Write([]byte("event: end_of_stream\n\n"))
	}))
	defer upstream.Close()

	req := &Request{
		Body:      []byte(`{"model":"pplx-auto","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		ModelName: "pplx-auto",
		Config:    pplxTestConfig(upstream.URL),
		APIKey:    "cookie-value",
		ConnData:  map[string]any{},
		IsStream:  true,
	}
	rec := httptest.NewRecorder()
	if err := ForwardPerplexityWeb(rec, req); err != nil {
		t.Fatalf("forward: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data: [DONE]") {
		t.Error("stream did not terminate with [DONE]")
	}
	var content strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") || strings.Contains(line, "[DONE]") {
			continue
		}
		var chunk map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk) != nil {
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
		if s, ok := delta["content"].(string); ok {
			content.WriteString(s)
		}
	}
	if got := content.String(); got != "Hello" {
		t.Errorf("streamed content = %q, want %q", got, "Hello")
	}
}

// An expired session answers 401, and the message says what to do about it.
func TestForwardPerplexityWebAuthFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()

	req := &Request{
		Body:      []byte(`{"model":"pplx-auto","messages":[{"role":"user","content":"hi"}]}`),
		ModelName: "pplx-auto",
		Config:    pplxTestConfig(upstream.URL),
		APIKey:    "stale",
		ConnData:  map[string]any{},
	}
	rec := httptest.NewRecorder()
	if err := ForwardPerplexityWeb(rec, req); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "session cookie") {
		t.Errorf("error does not say how to recover: %s", rec.Body.String())
	}
}

// A request with no messages is refused before anything reaches the network.
func TestForwardPerplexityWebRejectsEmptyMessages(t *testing.T) {
	req := &Request{
		Body:      []byte(`{"model":"pplx-auto","messages":[]}`),
		ModelName: "pplx-auto",
		Config:    pplxTestConfig("http://127.0.0.1:1"),
		ConnData:  map[string]any{},
	}
	rec := httptest.NewRecorder()
	if err := ForwardPerplexityWeb(rec, req); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// pplxTestConfig points the lane at a fake upstream.
func pplxTestConfig(baseURL string) *providers.ProviderConfig {
	return &providers.ProviderConfig{BaseURL: baseURL}
}
