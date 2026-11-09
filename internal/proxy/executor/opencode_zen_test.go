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
	"9router/proxy/internal/proxy"
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
			"User-Agent":        "opencode/1.19.4",
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

// The strict Responses APIs upstream of this lane reject a top-level
// reasoning_effort, so every effort has to be nested — and "none" has no nested
// form, so it must remove the field rather than send an effort the model would
// happily charge for.
func TestNormalizeZenResponsesBody_ReasoningShapes(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantEff string
		wantSum any
		wantNo  bool
	}{
		{
			name:    "chat effort nests with an auto summary",
			input:   `{"model":"muse-spark-1.3","reasoning_effort":"high"}`,
			wantEff: "high",
			wantSum: "auto",
		},
		{
			name:    "none removes the reasoning field entirely",
			input:   `{"model":"muse-spark-1.3","reasoning_effort":"none"}`,
			wantNo:  true,
			wantSum: nil,
		},
		{
			name:    "none is case and space insensitive",
			input:   `{"model":"muse-spark-1.3","reasoning_effort":" None "}`,
			wantNo:  true,
			wantSum: nil,
		},
		{
			name:    "an existing summary is preserved",
			input:   `{"model":"muse-spark-1.3","reasoning_effort":"low","reasoning":{"summary":"detailed"}}`,
			wantEff: "low",
			wantSum: "detailed",
		},
		{
			name:    "none drops a reasoning object the client had set too",
			input:   `{"model":"muse-spark-1.3","reasoning_effort":"none","reasoning":{"effort":"max","summary":"auto"}}`,
			wantNo:  true,
			wantSum: nil,
		},
		{
			name:    "a request with no effort is left alone",
			input:   `{"model":"muse-spark-1.3"}`,
			wantSum: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := normalizeZenResponsesBody([]byte(tt.input), "muse-spark-1.3")
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if _, ok := m["reasoning_effort"]; ok {
				t.Fatalf("a top-level reasoning_effort must never reach a strict Responses lane: %s", out)
			}
			reasoning, present := m["reasoning"]
			if tt.wantNo {
				if present {
					t.Fatalf("reasoning must be removed for effort none, got %v: %s", reasoning, out)
				}
				return
			}
			if tt.wantEff == "" {
				return
			}
			rm, ok := reasoning.(map[string]any)
			if !ok {
				t.Fatalf("reasoning = %v, want a nested object: %s", reasoning, out)
			}
			if rm["effort"] != tt.wantEff {
				t.Errorf("reasoning.effort = %v, want %q", rm["effort"], tt.wantEff)
			}
			if rm["summary"] != tt.wantSum {
				t.Errorf("reasoning.summary = %v, want %v", rm["summary"], tt.wantSum)
			}
		})
	}
}

// Paid ids stay on the keyed /zen/v1 lane with the v1 identity — including
// big-pickle, which has no live 403 evidence and the lane table carries as
// paid chat, so it must not be swept into the inference gate. The -free suffix
// rule added for #327 must not reach a bare paid id like this one.
func TestForwardOpencodeZen_KeyedLaneStaysOnZenV1(t *testing.T) {
	tests := []struct {
		model string
		want  string
		reply func(w http.ResponseWriter)
	}{
		{"big-pickle", "/zen/v1/chat/completions", zenChatReply},
		{"deepseek-v4-pro", "/zen/v1/chat/completions", zenChatReply},
		{"grok-4.6", "/zen/v1/responses", zenResponsesReply},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			var path, ua, client string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				ua = r.Header.Get("User-Agent")
				client = r.Header.Get("x-opencode-client")
				tt.reply(w)
			}))
			defer srv.Close()
			rec := httptest.NewRecorder()
			req := &Request{
				Ctx:    context.Background(),
				Client: srv.Client(),
				Config: &providers.ProviderConfig{
					BaseURL:    srv.URL + "/zen/v1/chat/completions",
					AuthHeader: "Authorization",
					AuthScheme: "bearer",
					StaticHeaders: map[string]string{
						"x-opencode-client": "desktop",
						"User-Agent":        "opencode/1.19.4",
					},
				},
				APIKey:        "sk-zen",
				Body:          []byte(`{"model":"` + tt.model + `","messages":[{"role":"user","content":"hi"}],"stream":true}`),
				IsStream:      true,
				TranslateResp: false,
			}
			if err := ForwardOpencodeZen(rec, req); err != nil {
				t.Fatalf("ForwardOpencodeZen: %v", err)
			}
			if path != tt.want {
				t.Errorf("model %q reached %q, want the keyed lane %q", tt.model, path, tt.want)
			}
			if ua != proxy.DefaultOpenCodeV1UA {
				t.Errorf("model %q sent User-Agent %q, want the v1 identity %q", tt.model, ua, proxy.DefaultOpenCodeV1UA)
			}
			if client != "desktop" {
				t.Errorf("model %q sent x-opencode-client %q, want desktop", tt.model, client)
			}
		})
	}
}

// Free-tier ids 403 on the keyed lane ("free tier can only be used from within
// OpenCode", probed live), so they travel exactly like free-executor requests:
// inference endpoint, Bearer public, v2 cli identity and fingerprint.
// The shaper must follow the gate the request lands on, not the lane it arrived
// from. A free id is forwarded to the keyless inference gate from every lane,
// and that gate wants the genuine CLI 2.x read/shell definitions; the legacy
// quartet is the /zen/v1 fingerprint and is refused there.
//
// This pins the rule directly rather than through a lane, because no free id is
// served on the messages lane today — the bug this guards (the messages lane
// shaping every request as paid) was therefore invisible to every lane test.
func TestZenShapedBodyFollowsTheDestinationGate(t *testing.T) {
	client := []byte(`{"model":"x","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"Bash","parameters":{"type":"object"}}}]}`)

	t.Run("free id gets the CLI 2.x fingerprint", func(t *testing.T) {
		out, _ := zenShapedBody(client, "deepseek-v4-flash-free", true)
		var payload map[string]any
		if err := json.Unmarshal(out, &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		raw, _ := payload["tools"].([]any)
		names := map[string]bool{}
		for _, tool := range raw {
			if m, ok := tool.(map[string]any); ok {
				if fn, ok := m["function"].(map[string]any); ok {
					if n, ok := fn["name"].(string); ok {
						names[n] = true
					}
				}
			}
		}
		if !names["read"] || !names["shell"] {
			t.Errorf(`a free request must carry the genuine "read" and "shell" fingerprint, got %v`, names)
		}
		if names["Bash"] || names["bash"] {
			t.Errorf("a caller tool must not travel as a declaration, got %v", names)
		}
		if len(raw) != 12 {
			t.Errorf("a free request must declare exactly the 12 genuine built-ins, got %d", len(raw))
		}
		system, _ := payload["messages"].([]any)[0].(map[string]any)["content"].(string)
		if !strings.Contains(system, `"name":"Bash"`) {
			t.Errorf("the caller Bash tool must be described in the system text: %s", out)
		}
	})

	t.Run("paid id keeps the legacy quartet collapse", func(t *testing.T) {
		out, nameMap := zenShapedBody(client, "deepseek-v4-pro", false)
		if nameMap["bash"] != "Bash" {
			t.Errorf("paid request must collapse Bash -> bash, got map %v", nameMap)
		}
		if !strings.Contains(string(out), `"name":"bash"`) {
			t.Errorf("paid request must declare bash, got %s", out)
		}
	})
}

func TestForwardOpencodeZen_FreeModelTakesInferenceGate(t *testing.T) {
	tests := []struct {
		model string
		want  string
		reply func(w http.ResponseWriter)
	}{
		{"deepseek-v4-flash-free", "/inference/openai/v1/chat/completions", zenChatReply},
		{"muse-spark-1.3-contributor-free", "/inference/openai/v1/responses", zenResponsesReply},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			var path, auth, ua, client string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				auth = r.Header.Get("Authorization")
				ua = r.Header.Get("User-Agent")
				client = r.Header.Get("x-opencode-client")
				tt.reply(w)
			}))
			defer srv.Close()
			rec := httptest.NewRecorder()
			req := &Request{
				Ctx:    context.Background(),
				Client: srv.Client(),
				Config: &providers.ProviderConfig{
					BaseURL:    srv.URL + "/zen/v1/chat/completions",
					AuthHeader: "Authorization",
					AuthScheme: "bearer",
					StaticHeaders: map[string]string{
						"x-opencode-client": "desktop",
						"User-Agent":        "opencode/1.19.4",
					},
				},
				APIKey:        "sk-zen",
				Body:          []byte(`{"model":"` + tt.model + `","messages":[{"role":"user","content":"hi"}],"stream":true}`),
				IsStream:      true,
				TranslateResp: false,
			}
			if err := ForwardOpencodeZen(rec, req); err != nil {
				t.Fatalf("ForwardOpencodeZen: %v", err)
			}
			if path != tt.want {
				t.Errorf("model %q reached %q, want the inference gate %q", tt.model, path, tt.want)
			}
			if auth != "Bearer public" {
				t.Errorf("model %q sent %q, want the public credential the gate expects", tt.model, auth)
			}
			if ua != proxy.DefaultOpenCodeUA {
				t.Errorf("model %q sent User-Agent %q, want the v2 identity %q", tt.model, ua, proxy.DefaultOpenCodeUA)
			}
			if client != "cli" {
				t.Errorf("model %q sent x-opencode-client %q, want cli", tt.model, client)
			}
		})
	}
}

// A free-tier id on the messages lane travels through the same stealth-capable
// forwarder as the other free lanes: the legacy /inference/v1 prefix — the v2
// gate's /inference/openai/v1 surface has no /messages route (404), so only
// the legacy prefix serves this lane — the public credential, the v2 cli
// identity, and the read/shell fingerprint. It used to take the plain
// DoRequest path, which the gate refuses on a direct dial — the request only
// passed when an edge relay made the call.
func TestForwardZenMessages_FreeLaneTakesTheStealthForwarder(t *testing.T) {
	probe := newZenProbe(t, zenMessagesReply)
	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      context.Background(),
		Client:   probe.server.Client(),
		Config:   zenConfig(probe),
		APIKey:   "sk-zen",
		Body:     []byte(`{"model":"claude-sonnet-4-6-free","max_tokens":256,"system":"be brief","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		IsStream: true,
	}
	if err := forwardZenMessages(rec, req, "claude-sonnet-4-6-free", true, providers.FormatClaude); err != nil {
		t.Fatalf("forwardZenMessages: %v", err)
	}
	if probe.path != "/inference/v1/messages" {
		t.Errorf("expected the legacy inference messages lane, got %q", probe.path)
	}
	if probe.auth != "Bearer public" {
		t.Errorf("expected the public credential, got %q", probe.auth)
	}
	system, _ := probe.body["system"].(string)
	if !strings.HasPrefix(system, "You are an AI agent running in OpenCode, a coding agent harness.") {
		t.Errorf("a native Claude system must gain the harness preamble, got %.80s", system)
	}
	if !strings.Contains(system, "be brief") || !strings.Contains(system, "No tools are available") {
		t.Errorf("the caller system text and the placeholder note must both survive, got tail %.60s", system[len(system)-60:])
	}
	tools, _ := probe.body["tools"].([]any)
	if len(tools) != 12 {
		t.Errorf("the messages lane must declare exactly the 12 genuine built-ins, got %d", len(tools))
	}
	for _, tool := range tools {
		tm, _ := tool.(map[string]any)
		if _, ok := tm["input_schema"]; !ok {
			t.Errorf("claude-shaped tool lost input_schema: %v", tool)
		}
	}
}
