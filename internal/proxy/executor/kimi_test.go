package executor

import (
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

// kimiProbe is a fake upstream that records where the gateway sent a request.
type kimiProbe struct {
	server *httptest.Server
	path   string
	body   map[string]any
}

func newKimiProbe(t *testing.T, reply func(w http.ResponseWriter)) *kimiProbe {
	t.Helper()
	p := &kimiProbe{}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &p.body)
		reply(w)
	}))
	t.Cleanup(p.server.Close)
	return p
}

func kimiTestConfig(probe *kimiProbe) *providers.ProviderConfig {
	return &providers.ProviderConfig{
		BaseURL:    probe.server.URL + "/coding/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	}
}

func kimiChatReply(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"kimi-ok\"}}]}\n\ndata: [DONE]\n\n"))
}

func kimiResponsesReply(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"output\":[]}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"kimi-ok\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"kimi-ok\"}]}]}}\n\n"))
}

// A /v1/responses client must reach the native /responses lane with a
// Responses-shaped body, not be downgraded to the single chat URL.
func TestForwardKimi_ResponsesClientKeepsResponsesLane(t *testing.T) {
	probe := newKimiProbe(t, kimiResponsesReply)
	rec := httptest.NewRecorder()
	ctx := translator.WithClientFormat(context.Background(), translator.ClientFormatResponses)
	req := &Request{
		Ctx:      ctx,
		Client:   probe.server.Client(),
		Config:   kimiTestConfig(probe),
		APIKey:   "sk-kimi",
		Body:     []byte(`{"model":"kimi/kimi-k3","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true}`),
		IsStream: true,
	}
	if err := ForwardKimi(rec, req); err != nil {
		t.Fatalf("ForwardKimi: %v", err)
	}
	if probe.path != "/coding/v1/responses" {
		t.Fatalf("a Responses client must hit the native lane, got %q", probe.path)
	}
	if _, ok := probe.body["input"]; !ok {
		t.Errorf("responses lane must receive input[], got %v", probe.body)
	}
	if probe.body["model"] != "kimi-k3" {
		t.Errorf("the provider prefix must be stripped, got %v", probe.body["model"])
	}
	if _, ok := probe.body["messages"]; ok {
		t.Errorf("a Responses body must not be downgraded to chat, got %v", probe.body)
	}
	if !strings.Contains(rec.Body.String(), "kimi-ok") {
		t.Errorf("served body must carry upstream text, got %q", rec.Body.String())
	}
}

// A Chat Completions client keeps the chat lane its model declares, even though
// Responses is the model's target lane.
func TestForwardKimi_ChatClientKeepsChatLane(t *testing.T) {
	probe := newKimiProbe(t, kimiChatReply)
	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      context.Background(),
		Client:   probe.server.Client(),
		Config:   kimiTestConfig(probe),
		APIKey:   "sk-kimi",
		Body:     []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		IsStream: true,
	}
	if err := ForwardKimi(rec, req); err != nil {
		t.Fatalf("ForwardKimi: %v", err)
	}
	if probe.path != "/coding/v1/chat/completions" {
		t.Fatalf("a chat client must stay on the chat lane, got %q", probe.path)
	}
	if _, ok := probe.body["input"]; ok {
		t.Errorf("chat lane must receive messages[], not Responses input[]: %v", probe.body)
	}
	if !strings.Contains(rec.Body.String(), "kimi-ok") {
		t.Errorf("served body must carry upstream text, got %q", rec.Body.String())
	}
}

// A chat client asking for an id the registry does not publish still reaches the
// Responses lane — every Kimi Code model serves it natively — so its body is
// converted rather than POSTed to the chat URL with a Responses shape.
func TestForwardKimi_ChatClientForUndeclaredModelIsConverted(t *testing.T) {
	probe := newKimiProbe(t, kimiResponsesReply)
	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      context.Background(),
		Client:   probe.server.Client(),
		Config:   kimiTestConfig(probe),
		APIKey:   "sk-kimi",
		Body:     []byte(`{"model":"kimi-k3-imaginary","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		IsStream: true,
	}
	if err := ForwardKimi(rec, req); err != nil {
		t.Fatalf("ForwardKimi: %v", err)
	}
	if probe.path != "/coding/v1/responses" {
		t.Fatalf("an undeclared id takes the responses lane, got %q", probe.path)
	}
	if _, ok := probe.body["input"]; !ok {
		t.Errorf("chat client body must be converted to Responses input[], got %v", probe.body)
	}
	if probe.body["store"] != false {
		t.Errorf("expected store:false, got %v", probe.body["store"])
	}
}

// The lane URL is derived from the configured base URL, never a hardcoded host:
// a connection override that already names /responses stays on that host.
func TestForwardKimi_DerivesLaneFromConfiguredBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		wantBase string
	}{
		{name: "registry default names the chat lane", baseURL: "https://api.kimi.com/coding/v1/chat/completions", wantBase: "https://api.kimi.com/coding/v1"},
		{name: "override already names the responses lane", baseURL: "https://relay.example/coding/v1/responses", wantBase: "https://relay.example/coding/v1"},
		{name: "bare prefix is left alone", baseURL: "https://relay.example/coding/v1", wantBase: "https://relay.example/coding/v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &providers.ProviderConfig{BaseURL: tt.baseURL}
			if got := kimiBaseURL(cfg); got != tt.wantBase {
				t.Errorf("kimiBaseURL(%q) = %q, want %q", tt.baseURL, got, tt.wantBase)
			}
		})
	}
}

// A non-streaming caller on the Responses lane still gets a re-aggregated body:
// the lane is always asked for the event stream.
func TestForwardKimi_NonStreamingResponsesClientIsAggregated(t *testing.T) {
	probe := newKimiProbe(t, kimiResponsesReply)
	rec := httptest.NewRecorder()
	ctx := translator.WithClientFormat(context.Background(), translator.ClientFormatResponses)
	req := &Request{
		Ctx:    ctx,
		Client: probe.server.Client(),
		Config: kimiTestConfig(probe),
		APIKey: "sk-kimi",
		Body:   []byte(`{"model":"kimi-k3","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
	}
	if err := ForwardKimi(rec, req); err != nil {
		t.Fatalf("ForwardKimi: %v", err)
	}
	if probe.path != "/coding/v1/responses" {
		t.Fatalf("expected responses lane, got %q", probe.path)
	}
	if probe.body["stream"] != true {
		t.Errorf("the responses lane is always asked to stream, got %v", probe.body["stream"])
	}
	if !strings.Contains(rec.Body.String(), "kimi-ok") {
		t.Errorf("aggregated body must carry upstream text, got %q", rec.Body.String())
	}
}