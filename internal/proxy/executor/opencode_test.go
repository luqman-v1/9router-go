package executor

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

func TestInjectReasoningContent(t *testing.T) {
	input := []byte(`{
		"model": "deepseek-v4-flash-free",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": "hi there"}
		]
	}`)

	res := InjectReasoningContent(input, "opencode")

	var reqMap map[string]any
	if err := json.Unmarshal(res, &reqMap); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}

	msgs := reqMap["messages"].([]any)
	asst := msgs[1].(map[string]any)
	if rc, ok := asst["reasoning_content"].(string); !ok || rc != " " {
		t.Errorf("expected reasoning_content to be ' ', got %v", asst["reasoning_content"])
	}
}

func TestForwardOpencode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer public" {
			t.Errorf("expected Bearer public, got %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("x-opencode-client") != "desktop" {
			t.Errorf("expected desktop client header, got %s", r.Header.Get("x-opencode-client"))
		}
		if r.Header.Get("x-opencode-project") != "global" {
			t.Errorf("expected x-opencode-project global, got %s", r.Header.Get("x-opencode-project"))
		}
		if !strings.HasPrefix(r.Header.Get("x-opencode-session"), "ses_") {
			t.Errorf("expected ses_ prefix on x-opencode-session, got %s", r.Header.Get("x-opencode-session"))
		}
		if !strings.HasPrefix(r.Header.Get("x-opencode-request"), "msg_") {
			t.Errorf("expected msg_ prefix on x-opencode-request, got %s", r.Header.Get("x-opencode-request"))
		}
		if r.Header.Get("User-Agent") != "opencode" {
			t.Errorf("expected User-Agent opencode, got %s", r.Header.Get("User-Agent"))
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"reasoning_content":" "`) {
			t.Errorf("expected reasoning_content injected in request body, got %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"msg_123","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}

	rec := httptest.NewRecorder()
	req := &Request{
		Client:        srv.Client(),
		Config:        cfg,
		APIKey:        "", // Should fallback to "public"
		Body:          []byte(`{"model":"deepseek-v4-flash-free","messages":[{"role":"assistant","content":"prev"}]}`),
		IsStream:      false,
		TranslateResp: false,
	}

	err := ForwardOpencode(rec, req)
	if err != nil {
		t.Fatalf("ForwardOpencode failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

func TestForwardOpencodeGo_OpenAIRouting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-go-key" {
			t.Errorf("expected Authorization Bearer secret-go-key header, got %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"msg_openai","choices":[{"message":{"content":"hello"}}]}`))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}

	rec := httptest.NewRecorder()
	req := &Request{
		Client:        srv.Client(),
		Config:        cfg,
		APIKey:        "secret-go-key",
		Body:          []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`),
		IsStream:      false,
		TranslateResp: false,
	}

	err := ForwardOpencodeGo(rec, req)
	if err != nil {
		t.Fatalf("ForwardOpencodeGo failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

func TestForwardOpencode_MuseSparkResponsesRouting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("expected path ending in /responses, got %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if parsed["input"] == nil {
			t.Errorf("expected input array in Responses API format, got: %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resp_123","output":[{"type":"message","content":[{"type":"text","text":"4"}]}]}`))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL + "/chat/completions",
	}

	rec := httptest.NewRecorder()
	req := &Request{
		Client:        srv.Client(),
		Config:        cfg,
		APIKey:        "",
		Body:          []byte(`{"model":"muse-spark-1.2-contributor-free","messages":[{"role":"user","content":"2+2?"}],"reasoning_effort":"max"}`),
		IsStream:      false,
		TranslateResp: false,
	}

	err := ForwardOpencode(rec, req)
	if err != nil {
		t.Fatalf("ForwardOpencode muse-spark failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}
