package executor

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"9router/proxy/internal/providers"
)

// The envelope is Perch's, not OpenAI's: the request body nests under
// `request`, keeps the caller's maxTokens / toolChoice spelling, and carries
// the model choice as a Roost tier or a pinned manual id.
func TestPerchEnvelopeShape(t *testing.T) {
	body := map[string]any{
		"messages":         []any{map[string]any{"role": "user", "content": "hi"}},
		"max_tokens":       float64(64),
		"temperature":      float64(0.5),
		"tools":            []any{map[string]any{"type": "function"}},
		"reasoning_effort": "low",
	}
	env := perchEnvelope("anthropic-claude-sonnet-5-5", body, "run-1")
	reqMap, ok := env["request"].(map[string]any)
	if !ok {
		t.Fatalf("request is %T", env["request"])
	}
	if reqMap["maxTokens"] != float64(64) {
		t.Errorf("maxTokens = %v, want 64", reqMap["maxTokens"])
	}
	if reqMap["temperature"] != float64(0.5) {
		t.Errorf("temperature = %v", reqMap["temperature"])
	}
	if env["manualModelOptionId"] != "anthropic-claude-sonnet-5-5" {
		t.Errorf("manualModelOptionId = %v", env["manualModelOptionId"])
	}
	if env["runId"] != "run-1" || env["lane"] != "chat" || env["clientSurface"] != "cli" {
		t.Errorf("envelope = %v", env)
	}
	if env["effort"].(map[string]any)["level"] != "low" {
		t.Errorf("effort = %v", env["effort"])
	}

	roost := perchEnvelope("perch-auto", body, "run-2")
	if roost["roostModelChoice"] != "standard" {
		t.Errorf("roostModelChoice = %v, want standard", roost["roostModelChoice"])
	}
	if roost["manualModelOptionId"] != nil {
		t.Errorf("manualModelOptionId on a Roost tier = %v", roost["manualModelOptionId"])
	}
}

// A ticket body the gateway does not understand is not a ticket at all, and
// the mint refuses it instead of sending a maluformed header upstream.
func TestPerchMintTicketRejectsMalformedTicket(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ticket":"x"}`))
	}))
	defer upstream.Close()

	_, err := perchMintTicket(context.Background(), http.DefaultClient, upstream.URL, "tok", "")
	if err == nil {
		t.Fatal("expected an error on a malformed ticket body")
	}
	if !strings.Contains(err.Error(), "incomplete or expired") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestPerchMintTicketRejectsForeignSupabaseConfig(t *testing.T) {
	// The config gate is the onlyбеж guard between a saved refresh token and
	// the host it is POSTed to; a config naming another project must stop
	// before any token moves. Here we only assert the executor-level helpers
	// never invent a fallback host for credentials.
	if got := perchOrigin("https://app.perchai.app/api/perch-terminal/model-call"); got != providers.PerchAppURL {
		t.Errorf("origin = %q, want %q", got, providers.PerchAppURL)
	}
	if got := perchOrigin("https://evil.test/api/perch-terminal/model-call"); got != providers.PerchAppURL {
		t.Errorf("foreign baseURL resolved to %q", got)
	}
}

// Sessions minted under one scope must not leak into the next: each scope
// gets its own queue and its own ticket.
func TestPerchTurnScopesAreIsolated(t *testing.T) {
	var mu sync.Mutex
	var tickets int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tickets++
		n := tickets
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ticket":"t` + string(rune('a'+n-1)) + `","ticketId":"t` + string(rune('a'+n-1)) + `","runId":"r` + string(rune('a'+n-1)) + `","expiresAt":"2999-01-01T00:00:00Z"}`))
	}))
	defer upstream.Close()

	a, err := perchAcquireTurn(context.Background(), http.DefaultClient, upstream.URL, "tok", "scope-a", "")
	if err != nil {
		t.Fatalf("scope-a: %v", err)
	}
	b, err := perchAcquireTurn(context.Background(), http.DefaultClient, upstream.URL, "tok", "scope-b", "")
	if err != nil {
		t.Fatalf("scope-b: %v", err)
	}
	if a.ticket == b.ticket {
		t.Errorf("two scopes shared a ticket: %q", a.ticket)
	}
}

// The streamed form turns Perch's reasoning_delta / answer_delta into OpenAI
// chunks and closes on `done` with usage; buffered form is the same answer in
// one completion.
func TestForwardPerchNonStream(t *testing.T) {
	upstream := perchTestUpstream(t, `data: {"type":"reasoning_delta","text":"think"}`+"\n\n"+
		`data: {"type":"answer_delta","text":"Hello "}`+"\n\n"+
		`data: {"type":"answer_delta","text":"perch."}`+"\n\n"+
		`data: {"type":"done","ok":true,"text":"Hello perch.","model":"anthropic-claude-sonnet-5-5","usage":{"inputTokens":10,"cacheReadInputTokens":4,"outputTokens":3,"reasoningTokens":2}}`+"\n\n")
	defer upstream.Close()

	req := &Request{
		Body:      []byte(`{"model":"perch/anthropic-claude-sonnet-5-5","messages":[{"role":"user","content":"hi"}]}`),
		ModelName: "perch/anthropic-claude-sonnet-5-5",
		Config:    &providers.ProviderConfig{BaseURL: upstream.URL + "/api/perch-terminal/model-call"},
		APIKey:    "test-token",
		ConnData:  map[string]any{},
	}
	rec := httptest.NewRecorder()
	if err := ForwardPerch(rec, req); err != nil {
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
	if content, _ := msg["content"].(string); content != "Hello perch." {
		t.Errorf("content = %q", content)
	}
	usage := out["usage"].(map[string]any)
	if usage["prompt_tokens"] != float64(14) || usage["completion_tokens"] != float64(3) {
		t.Errorf("usage = %v", usage)
	}
}

func TestForwardPerchStream(t *testing.T) {
	upstream := perchTestUpstream(t, `data: {"type":"answer_delta","text":"Hel"}`+"\n\n"+
		`data: {"type":"answer_delta","text":"lo"}`+"\n\n"+
		`data: {"type":"done","ok":true,"text":"Hello","model":"anthropic-claude-sonnet-5-5"}`+"\n\n")
	defer upstream.Close()

	req := &Request{
		Body:      []byte(`{"model":"perch/anthropic-claude-sonnet-5-5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		ModelName: "perch/anthropic-claude-sonnet-5-5",
		Config:    &providers.ProviderConfig{BaseURL: upstream.URL + "/api/perch-terminal/model-call"},
		APIKey:    "test-token",
		ConnData:  map[string]any{},
		IsStream:  true,
	}
	rec := httptest.NewRecorder()
	if err := ForwardPerch(rec, req); err != nil {
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

// Usage arithmetic: cached input is inside prompt_tokens, and reasoning
// tokens stay a subset of output instead of being added twice.
func TestPerchUsage(t *testing.T) {
	got := perchUsage(map[string]any{"inputTokens": float64(10), "cacheReadInputTokens": float64(4), "outputTokens": float64(5), "reasoningTokens": float64(2)})
	if got["prompt_tokens"] != float64(14) {
		t.Errorf("prompt_tokens = %v", got["prompt_tokens"])
	}
	if got["completion_tokens"] != float64(5) {
		t.Errorf("completion_tokens = %v", got["completion_tokens"])
	}
	if got["total_tokens"] != float64(19) {
		t.Errorf("total_tokens = %v", got["total_tokens"])
	}
}

// perchTestUpstream serves the turn-ticket mint and then the model-call
// stream, asserting the CLI surface headers the proxy requires.
func perchTestUpstream(t *testing.T, stream string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Helper()
		switch r.URL.Path {
		case perchTurnTicketPath:
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Errorf("turn-ticket Authorization = %q", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ticket":"ticket-value","ticketId":"id-1","runId":"run-1","expiresAt":"2999-01-01T00:00:00Z"}`))
			return
		case perchModelCallPath:
			if r.Header.Get("x-perch-turn-ticket") != "ticket-value" {
				t.Errorf("x-perch-turn-ticket = %q", r.Header.Get("x-perch-turn-ticket"))
			}
			if r.Header.Get("User-Agent") != perchCLIUserAgent {
				t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
			}
			var body map[string]any
			if err := json.UnmarshalRead(r.Body, &body); err != nil {
				t.Errorf("model-call body is not JSON: %v", err)
			}
			if body["clientSurface"] != "cli" {
				t.Errorf("clientSurface = %v", body["clientSurface"])
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(stream))
			return
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}
