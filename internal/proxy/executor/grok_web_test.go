package executor

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	json "encoding/json/v2"
)

// grokTestEnv aims the executor at a fake conversations/new endpoint through
// the plain doer, so the whole turn — headers, payload, NDJSON handling — is
// exercised without the browser-fingerprint TLS stack.
func grokTestEnv(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	site := httptest.NewServer(handler)
	t.Cleanup(site.Close)
	prevURL, prevFactory := grokWebChatURL, grokTransportFactory
	grokWebChatURL = site.URL
	grokTransportFactory = func(*http.Client, string) (grokDoer, error) { return &grokPlainDoer{}, nil }
	t.Cleanup(func() { grokWebChatURL, grokTransportFactory = prevURL, prevFactory })
	return site
}

func TestGrokBuildCookieHeader(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"bare sso value", "eyJabc", "sso=eyJabc"},
		{"single pair", "sso=eyJabc", "sso=eyJabc"},
		{"full blob with pairs", "foo=1; sso=eyJabc; sso-rw=rw1; cf_clearance=cf1; other=2", "sso=eyJabc; sso-rw=rw1; cf_clearance=cf1"},
		{"bearer prefix", "bearer sso=eyJabc", "sso=eyJabc"},
		{"cookie label", "cookie: sso=eyJabc", "sso=eyJabc"},
		{"no sso", "foo=1; bar=2", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		if got := grokBuildCookieHeader(tt.raw); got != tt.want {
			t.Errorf("%s: grokBuildCookieHeader(%q) = %q, want %q", tt.name, tt.raw, got, tt.want)
		}
	}
}

func TestGrokStatsigID(t *testing.T) {
	decoded, err := base64.StdEncoding.DecodeString(grokStatsigID())
	if err != nil {
		t.Fatalf("statsig id is not base64: %v", err)
	}
	if !strings.HasPrefix(string(decoded), "x1:TypeError: Cannot read properties of ") {
		t.Errorf("statsig id payload = %q", decoded)
	}
	// Two draws must differ — the payload is randomized per request.
	first, second := grokStatsigID(), grokStatsigID()
	if first == second {
		t.Error("statsig id must be randomized per call")
	}
}

func TestGrokResolveModel(t *testing.T) {
	route := grokResolveModel("grok-4-heavy")
	if route.grokModel != "grok-4-1-thinking-1129" || route.modelMode != "MODEL_MODE_EXPERT" || !route.isThinking {
		t.Errorf("grok-4-heavy route = %+v", route)
	}
	if got := grokResolveModel("grok-4.20").modelMode; got != "MODEL_MODE_GROK_420" {
		t.Errorf("grok-4.20 mode = %q", got)
	}
	if got := grokResolveModel("not-a-model").modelMode; got != "MODEL_MODE_FAST" {
		t.Errorf("unmapped model must default to FAST, got %q", got)
	}
	for name := range grokModelMap {
		if grokModelMap[name].grokModel != "grok-4-1-thinking-1129" {
			t.Errorf("%s routes to a model the endpoint refuses: %s", name, grokModelMap[name].grokModel)
		}
	}
}

func TestGrokParseMessages(t *testing.T) {
	messages := []any{
		map[string]any{"role": "system", "content": "be brief"},
		map[string]any{"role": "developer", "content": "dev only"},
		map[string]any{"role": "user", "content": "first question"},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "an answer"}}},
		map[string]any{"role": "user", "content": "second question"},
	}
	got := grokParseMessages(messages)
	want := "system: be brief\n\nsystem: dev only\n\nuser: first question\n\nassistant: an answer\n\nsecond question"
	if got != want {
		t.Errorf("grokParseMessages =\n%q\nwant\n%q", got, want)
	}
}

func grokChatRequest(t *testing.T, body string, cookie string, stream bool) *Request {
	t.Helper()
	ttft := int64(0)
	return &Request{
		Ctx:      t.Context(),
		Body:     []byte(body),
		APIKey:   cookie,
		IsStream: stream,
		TTFT:     &ttft,
	}
}

func TestForwardGrokWebStream(t *testing.T) {
	ndjson := strings.Join([]string{
		`{"result":{"response":{"token":"Hello"}}}`,
		`{"result":{"response":{"token":" world"}}}`,
		`{"result":{"response":{"modelResponse":{"message":"Hello world"}}}}`,
		`{"result":{"response":{"token":"!"}}}`,
	}, "\n")
	grokTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Cookie"); got != "sso=abc; sso-rw=rw" {
			t.Errorf("cookie header = %q", got)
		}
		if got := r.Header.Get("x-statsig-id"); got == "" {
			t.Error("x-statsig-id missing")
		}
		if got := r.Header.Get("traceparent"); !strings.HasPrefix(got, "00-") {
			t.Errorf("traceparent = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(ndjson))
	})

	rec := httptest.NewRecorder()
	req := grokChatRequest(t, `{"model":"grok-4.1-fast","messages":[{"role":"user","content":"hi"}]}`, "sso=abc; sso-rw=rw", true)
	if err := ForwardGrokWeb(rec, req); err != nil {
		t.Fatalf("ForwardGrokWeb: %v", err)
	}
	body := rec.Body.String()
	for _, want := range []string{`"role":"assistant"`, `"content":"Hello"`, `"content":" world"`, `"content":"Hello world"`, `"content":"!"`, `"finish_reason":"stop"`, "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream body missing %q:\n%s", want, body)
		}
	}
}

func TestForwardGrokWebNonStream(t *testing.T) {
	ndjson := strings.Join([]string{
		`{"result":{"response":{"token":"Hel"}}}`,
		`{"result":{"response":{"token":"lo"}}}`,
	}, "\n")
	grokTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(ndjson))
	})

	rec := httptest.NewRecorder()
	req := grokChatRequest(t, `{"model":"grok-4","messages":[{"role":"user","content":"hi"}]}`, "sso=abc", false)
	if err := ForwardGrokWeb(rec, req); err != nil {
		t.Fatalf("ForwardGrokWeb: %v", err)
	}
	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse response: %v (%s)", err, rec.Body.String())
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != "Hello" || out.Choices[0].FinishReason != "stop" {
		t.Errorf("response = %s", rec.Body.String())
	}
}

func TestForwardGrokWebRefusals(t *testing.T) {
	t.Run("cloudflare challenge", func(t *testing.T) {
		grokTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("<html><title>Just a moment...</title></html>"))
		})
		err := ForwardGrokWeb(httptest.NewRecorder(), grokChatRequest(t, `{"model":"grok-4","messages":[{"role":"user","content":"hi"}]}`, "sso=abc", false))
		if err == nil || !strings.Contains(err.Error(), "Cloudflare challenge") {
			t.Fatalf("err = %v, want Cloudflare challenge", err)
		}
	})
	t.Run("missing cookie", func(t *testing.T) {
		err := ForwardGrokWeb(httptest.NewRecorder(), grokChatRequest(t, `{"model":"grok-4","messages":[{"role":"user","content":"hi"}]}`, "", false))
		if err == nil || !strings.Contains(err.Error(), "Cookie header") {
			t.Fatalf("err = %v, want cookie guidance", err)
		}
	})
	t.Run("empty messages", func(t *testing.T) {
		err := ForwardGrokWeb(httptest.NewRecorder(), grokChatRequest(t, `{"model":"grok-4","messages":[]}`, "sso=abc", false))
		if err == nil {
			t.Fatal("empty messages must be refused")
		}
	})
}

func TestForwardGrokWebToolMarkup(t *testing.T) {
	// The markup opens in one token and closes in the next, the way a real
	// token stream splits it — the holdback keeps the open block out of the
	// content channel and resolves it into tool_calls at the end.
	ndjson := strings.Join([]string{
		`{"result":{"response":{"token":"Let me search: <tool_call>{\"name\":\"web_search\",\"arguments\":{\"query\":\"go gopher\"}}"}}}`,
		`{"result":{"response":{"token":"</tool_call>"}}}`,
	}, "\n")
	grokTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if json.Unmarshal(body, &payload) != nil {
			t.Errorf("payload not JSON: %s", body)
		}
		if message, _ := payload["message"].(string); !strings.Contains(message, "CLIENT_TOOLS:") || !strings.Contains(message, "1. name: web_search") {
			t.Errorf("manifest not injected: %q", message)
		}
		_, _ = w.Write([]byte(ndjson))
	})

	rec := httptest.NewRecorder()
	req := grokChatRequest(t, `{"model":"grok-4","messages":[{"role":"user","content":"search go gopher"}],"tools":[{"type":"function","function":{"name":"web_search","description":"web search","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}}]}`, "sso=abc", true)
	if err := ForwardGrokWeb(rec, req); err != nil {
		t.Fatalf("ForwardGrokWeb: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"tool_calls"`) || !strings.Contains(body, `"name":"web_search"`) {
		t.Errorf("tool_calls missing from stream:\n%s", body)
	}
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Errorf("finish_reason tool_calls missing:\n%s", body)
	}
	if strings.Contains(body, "<tool_call>") {
		t.Errorf("markup leaked to the client:\n%s", body)
	}
}

func TestForwardGrokWebNativeCard(t *testing.T) {
	ndjson := strings.Join([]string{
		`{"result":{"response":{"toolUsageCardId":"card-1","toolUsageCard":{"bash":{"args":{"command":"ls -la"}}}}}}`,
		`{"result":{"response":{"token":"done"}}}`,
	}, "\n")
	grokTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(ndjson))
	})

	rec := httptest.NewRecorder()
	req := grokChatRequest(t, `{"model":"grok-4","messages":[{"role":"user","content":"list files"}],"tools":[{"type":"function","function":{"name":"bash","description":"run a shell command","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}}]}`, "sso=abc", true)
	if err := ForwardGrokWeb(rec, req); err != nil {
		t.Fatalf("ForwardGrokWeb: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"name":"bash"`) || !strings.Contains(body, `ls -la`) {
		t.Errorf("native card not mapped:\n%s", body)
	}
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Errorf("finish_reason tool_calls missing:\n%s", body)
	}
}
