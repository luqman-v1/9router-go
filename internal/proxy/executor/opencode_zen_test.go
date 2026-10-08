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

// zenProbe is a fake upstream that records where the gateway sent a request.
type zenProbe struct {
	server *httptest.Server
	path   string
	auth   string
	body   map[string]any
	raw    string
}

func newZenProbe(t *testing.T, reply func(w http.ResponseWriter)) *zenProbe {
	t.Helper()
	p := &zenProbe{}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.path = r.URL.Path
		p.auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		p.raw = string(raw)
		_ = json.Unmarshal(raw, &p.body)
		if r.Header.Get("x-api-key") != "" {
			p.auth = "x-api-key:" + r.Header.Get("x-api-key")
		}
		reply(w)
	}))
	t.Cleanup(p.server.Close)
	return p
}

func zenConfig(probe *zenProbe) *providers.ProviderConfig {
	return &providers.ProviderConfig{
		BaseURL:    probe.server.URL + "/zen/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		UsageURL:   providers.KnownProviders["opencode-zen"].UsageURL,
		StaticHeaders: map[string]string{
			"x-opencode-client": "desktop",
			"User-Agent":        "opencode/1.18.31",
		},
	}
}

func zenChatReply(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"zen-ok\"}}]}\n\ndata: [DONE]\n\n"))
}

func zenResponsesReply(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n\n"))
}

func zenMessagesReply(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"content\":[]}}\n\n"))
}

// A Chat Completions client asking for a chat-lane model must reach
// /chat/completions and see the upstream text.
func TestForwardOpencodeZen_ChatLane(t *testing.T) {
	probe := newZenProbe(t, zenChatReply)
	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      context.Background(),
		Client:   probe.server.Client(),
		Config:   zenConfig(probe),
		APIKey:   "sk-zen",
		Body:     []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		IsStream: true,
	}
	if err := ForwardOpencodeZen(rec, req); err != nil {
		t.Fatalf("ForwardOpencodeZen: %v", err)
	}
	if probe.path != "/zen/v1/chat/completions" {
		t.Errorf("expected chat lane, got %q", probe.path)
	}
	if probe.body["model"] != "deepseek-v4-pro" {
		t.Errorf("expected bare model id, got %v", probe.body["model"])
	}
	if probe.body["stream"] != true {
		t.Errorf("expected stream:true forced, got %v", probe.body["stream"])
	}
	if !strings.Contains(rec.Body.String(), "zen-ok") {
		t.Errorf("served body must carry upstream text, got %q", rec.Body.String())
	}
}

// Muse Spark lives on /responses; a Chat Completions client is translated in.
func TestForwardOpencodeZen_ResponsesLane(t *testing.T) {
	probe := newZenProbe(t, zenResponsesReply)
	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      context.Background(),
		Client:   probe.server.Client(),
		Config:   zenConfig(probe),
		APIKey:   "sk-zen",
		Body:     []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		IsStream: true,
	}
	if err := ForwardOpencodeZen(rec, req); err != nil {
		t.Fatalf("ForwardOpencodeZen: %v", err)
	}
	if probe.path != "/zen/v1/responses" {
		t.Errorf("expected responses lane, got %q", probe.path)
	}
	if _, ok := probe.body["input"]; !ok {
		t.Errorf("chat client body must be converted to Responses input[], got %v", probe.body)
	}
	if probe.body["store"] != false {
		t.Errorf("expected store:false, got %v", probe.body["store"])
	}
}

// A Claude model is served from /messages with the Claude key header, whatever
// the client's wire format.
func TestForwardOpencodeZen_MessagesLaneFromChatClient(t *testing.T) {
	probe := newZenProbe(t, zenMessagesReply)
	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      context.Background(),
		Client:   probe.server.Client(),
		Config:   zenConfig(probe),
		APIKey:   "sk-zen",
		Body:     []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		IsStream: true,
	}
	if err := ForwardOpencodeZen(rec, req); err != nil {
		t.Fatalf("ForwardOpencodeZen: %v", err)
	}
	if probe.path != "/zen/v1/messages" {
		t.Errorf("expected messages lane, got %q", probe.path)
	}
	if probe.auth != "x-api-key:sk-zen" {
		t.Errorf("expected raw x-api-key auth, got %q", probe.auth)
	}
	if probe.body["max_tokens"] == nil {
		t.Errorf("OpenAI body must be given max_tokens, got %v", probe.body)
	}
}

// A /v1/messages client keeps its own Claude format on the messages lane.
func TestForwardOpencodeZen_MessagesLaneFromClaudeClient(t *testing.T) {
	probe := newZenProbe(t, zenMessagesReply)
	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:           context.Background(),
		Client:        probe.server.Client(),
		Config:        zenConfig(probe),
		APIKey:        "sk-zen",
		Body:          []byte(`{"model":"claude-sonnet-4-6","max_tokens":256,"system":"be brief","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		IsStream:      true,
		TranslateResp: true,
	}
	if err := ForwardOpencodeZen(rec, req); err != nil {
		t.Fatalf("ForwardOpencodeZen: %v", err)
	}
	if probe.path != "/zen/v1/messages" {
		t.Errorf("expected messages lane, got %q", probe.path)
	}
	if probe.body["system"] != "be brief" {
		t.Errorf("a Claude client body must not be rewritten, got %v", probe.body)
	}
	if _, ok := probe.body["max_tokens"]; !ok {
		t.Errorf("max_tokens must survive on a native Claude body, got %v", probe.body)
	}
}

// A Responses client on a chat-lane model must not be relayed as if the
// upstream spoke Responses.
func TestForwardOpencodeZen_ChatLaneFromResponsesClient(t *testing.T) {
	probe := newZenProbe(t, zenChatReply)
	rec := httptest.NewRecorder()
	ctx := translator.WithClientFormat(context.Background(), translator.ClientFormatResponses)
	req := &Request{
		Ctx:      ctx,
		Client:   probe.server.Client(),
		Config:   zenConfig(probe),
		APIKey:   "sk-zen",
		Body:     []byte(`{"model":"deepseek-v4-pro","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true}`),
		IsStream: true,
	}
	if err := ForwardOpencodeZen(rec, req); err != nil {
		t.Fatalf("ForwardOpencodeZen: %v", err)
	}
	if probe.path != "/zen/v1/chat/completions" {
		t.Errorf("expected chat lane, got %q", probe.path)
	}
	if _, ok := probe.body["input"]; ok {
		t.Errorf("chat lane must receive messages[], not Responses input[]: %v", probe.body)
	}
}

// Prior-turn reasoning items cannot be validated across rotated accounts.
func TestNormalizeZenResponsesBody_StripsReasoningContinuity(t *testing.T) {
	in := `{"model":"muse-spark-1.3","input":[
		{"type":"reasoning","id":"rs_1","encrypted_content":"blob"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}],"encrypted_content":"blob"}],
		"reasoning_effort":"max","max_tokens":100,"store":true,"tool_choice":{"type":"function","name":"bash"}}`
	out, err := normalizeZenResponsesBody([]byte(in), "muse-spark-1.3")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	list, _ := m["input"].([]any)
	if len(list) != 1 {
		t.Fatalf("reasoning item must be dropped, got %d items: %s", len(list), out)
	}
	if _, ok := list[0].(map[string]any)["encrypted_content"]; ok {
		t.Errorf("encrypted_content must be stripped: %s", out)
	}
	if m["max_output_tokens"] == nil || m["max_tokens"] != nil {
		t.Errorf("output cap must move to max_output_tokens: %s", out)
	}
	if m["store"] != false {
		t.Errorf("store must be forced false: %s", out)
	}
	reasoning, _ := m["reasoning"].(map[string]any)
	if reasoning["effort"] != "max" || reasoning["summary"] != "auto" {
		t.Errorf("reasoning_effort must fold into a reasoning object: %s", out)
	}
	if tc, ok := m["tool_choice"]; !ok || tc != "auto" {
		t.Errorf("muse-spark-1.3 must send an explicit auto tool_choice: %s", out)
	}
}
