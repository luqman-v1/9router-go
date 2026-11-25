package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/acp/agy"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

// buildFakeAGY writes a tiny ACP-speaking Go binary used as AGY_PATH.
func buildFakeAGY(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "main.go")
	src := `package main
import ("bufio";"encoding/json";"fmt";"os")
func main(){
 r:=bufio.NewReader(os.Stdin); w:=bufio.NewWriter(os.Stdout); defer w.Flush()
 for { line,err:=r.ReadString('\n'); if err!=nil{return}
  var m map[string]any; if json.Unmarshal([]byte(line),&m)!=nil{continue}
  method,_:=m["method"].(string); id:=m["id"]
  switch method {
  case "initialize":
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"protocolVersion\":1,\"agentInfo\":{\"name\":\"fake\",\"version\":\"1\"}}}\n",id)
  case "session/new":
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"sessionId\":\"s-1\"}}\n",id)
  case "session/prompt":
   if p:=os.Getenv("AGY_ACP_PROMPT_CAPTURE"); p!="" { b,_:=json.Marshal(m["params"]); _=os.WriteFile(p,b,0o600) }
   fmt.Fprintln(w,"{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"PONG\"}}}")
   w.Flush()
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"stopReason\":\"end_turn\",\"content\":[{\"type\":\"text\",\"text\":\"PONG\"}]}}\n",id)
  }
  w.Flush()
 }
}
`
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	// go build appends no extension on Windows, so the exhaustive build
	// produced a bare "fakeagy" that exec could not start.
	bin := filepath.Join(dir, "fakeagy")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, srcPath)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake agy: %v: %s", err, out)
	}
	return bin
}

func TestForwardAGYACPNonStream(t *testing.T) {
	fake := buildFakeAGY(t)
	t.Cleanup(agy.DefaultPool().Close)
	t.Setenv("AGY_PATH", fake)
	body, _ := json.Marshal(map[string]any{
		"model":    "gemini-3.8-flash-high",
		"messages": []map[string]any{{"role": "user", "content": "ping"}},
		"stream":   false,
	})
	req := &Request{
		Ctx:          context.Background(),
		Config:       &providers.ProviderConfig{BaseURL: "agy-acp://acp/stdio"},
		Provider:     "agy-acp",
		APIKey:       "x",
		Body:         body,
		IsStream:     false,
		ConnectionID: "conn-1",
		ConnData:     map[string]any{},
		ACPCredentials: func(ctx context.Context, connData map[string]any, connectionID string) (string, string, time.Time, error) {
			return "access", "refresh", time.Now().Add(time.Hour), nil
		},
	}
	rec := httptest.NewRecorder()
	if err := ForwardAGYACP(rec, req); err != nil {
		t.Fatalf("ForwardAGYACP: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v: %s", err, rec.Body.String())
	}
	if len(out.Choices) == 0 || out.Choices[0].Message.Content != "PONGPONG" {
		// Streamed chunk + final result both recorded.
		t.Errorf("unexpected content: %s", rec.Body.String())
	}
}

func TestForwardAGYACPInjectsIdentityOnWire(t *testing.T) {
	fake := buildFakeAGY(t)
	t.Cleanup(agy.DefaultPool().Close)
	t.Setenv("AGY_PATH", fake)
	capture := filepath.Join(t.TempDir(), "prompt.json")
	t.Setenv("AGY_ACP_PROMPT_CAPTURE", capture)

	body, _ := json.Marshal(map[string]any{
		"model": "gemini-4-hydrogen-high",
		"messages": []map[string]any{
			{"role": "system", "content": "be terse"},
			{"role": "user", "content": "kamu model apa"},
		},
		"stream": false,
	})
	req := &Request{
		Ctx:          context.Background(),
		Config:       &providers.ProviderConfig{BaseURL: "agy-acp://acp/stdio"},
		Provider:     "agy-acp",
		APIKey:       "probe-key",
		Body:         body,
		ConnectionID: "conn-wire-identity",
		ConnData:     map[string]any{},
		ACPCredentials: func(ctx context.Context, connData map[string]any, connectionID string) (string, string, time.Time, error) {
			return "access", "refresh", time.Now().Add(time.Hour), nil
		},
	}
	rec := httptest.NewRecorder()
	if err := ForwardAGYACP(rec, req); err != nil {
		t.Fatalf("ForwardAGYACP: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("no ACP prompt captured: %v", err)
	}
	var params struct {
		Prompt []agy.PromptBlock `json:"prompt"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("decode captured prompt: %v", err)
	}
	if len(params.Prompt) != 1 {
		t.Fatalf("prompt blocks = %d, want 1", len(params.Prompt))
	}
	text := params.Prompt[0].Text
	identity := translator.AntigravityDefaultSystem
	if !strings.Contains(text, identity) {
		t.Fatalf("ACP wire prompt missing identity:\n%s", text)
	}
	identityAt := strings.Index(text, identity)
	systemAt := strings.Index(text, "[System]\nbe terse")
	userAt := strings.Index(text, "[User]\nkamu model apa")
	if systemAt < 0 || userAt < 0 || identityAt > systemAt || systemAt > userAt {
		t.Fatalf("bad order: identity=%d caller system=%d user=%d", identityAt, systemAt, userAt)
	}
}

func TestForwardAGYACPStream(t *testing.T) {
	fake := buildFakeAGY(t)
	t.Cleanup(agy.DefaultPool().Close)
	t.Setenv("AGY_PATH", fake)
	body, _ := json.Marshal(map[string]any{
		"model":    "gemini-3.8-flash-high",
		"messages": []map[string]any{{"role": "user", "content": "ping"}},
		"stream":   true,
	})
	req := &Request{
		Ctx:          context.Background(),
		Config:       &providers.ProviderConfig{BaseURL: "agy-acp://acp/stdio"},
		Provider:     "agy-acp",
		Body:         body,
		IsStream:     true,
		ConnectionID: "conn-2",
		ConnData:     map[string]any{},
		ACPCredentials: func(ctx context.Context, connData map[string]any, connectionID string) (string, string, time.Time, error) {
			return "access", "refresh", time.Now().Add(time.Hour), nil
		},
	}
	rec := httptest.NewRecorder()
	if err := ForwardAGYACP(rec, req); err != nil {
		t.Fatalf("ForwardAGYACP: %v", err)
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("content-type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "PONG") || !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Errorf("stream missing content/DONE: %s", rec.Body.String())
	}
}

func TestForwardAGYACPNonStreamClaude(t *testing.T) {
	fake := buildFakeAGY(t)
	t.Cleanup(agy.DefaultPool().Close)
	t.Setenv("AGY_PATH", fake)
	body, _ := json.Marshal(map[string]any{
		"model":      "gemini-3.8-flash-high",
		"messages":   []map[string]any{{"role": "user", "content": "ping"}},
		"stream":     false,
		"max_tokens": 64,
	})
	req := &Request{
		Ctx:           context.Background(),
		Config:        &providers.ProviderConfig{BaseURL: "agy-acp://acp/stdio"},
		Provider:      "agy-acp",
		Body:          body,
		IsStream:      false,
		ClaudeClient:  true,
		TranslateResp: true,
		ConnectionID:  "conn-claude-ns",
		ConnData:      map[string]any{},
		ACPCredentials: func(ctx context.Context, connData map[string]any, connectionID string) (string, string, time.Time, error) {
			return "access", "refresh", time.Now().Add(time.Hour), nil
		},
	}
	rec := httptest.NewRecorder()
	if err := ForwardAGYACP(rec, req); err != nil {
		t.Fatalf("ForwardAGYACP: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	raw := rec.Body.String()
	// A /v1/messages client must get a Claude Message, not a Chat Completions
	// body. Before the fix the executor wrote the OpenAI shape directly.
	if strings.Contains(raw, `"choices"`) || strings.Contains(raw, "chat.completion") {
		t.Fatalf("Claude client received OpenAI shape: %s", raw)
	}
	var msg struct {
		Type       string `json:"type"`
		Role       string `json:"role"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &msg); err != nil {
		t.Fatalf("decode Claude message: %v: %s", err, raw)
	}
	if msg.Type != "message" || msg.Role != "assistant" {
		t.Errorf("not a Claude assistant message: type=%q role=%q", msg.Type, msg.Role)
	}
	if msg.StopReason == "" {
		t.Errorf("missing stop_reason: %s", raw)
	}
	var text string
	for _, b := range msg.Content {
		if b.Type == "text" {
			text += b.Text
		}
	}
	if !strings.Contains(text, "PONG") {
		t.Errorf("Claude content missing answer: %s", raw)
	}
}

func TestForwardAGYACPStreamClaude(t *testing.T) {
	fake := buildFakeAGY(t)
	t.Cleanup(agy.DefaultPool().Close)
	t.Setenv("AGY_PATH", fake)
	body, _ := json.Marshal(map[string]any{
		"model":      "gemini-3.8-flash-high",
		"messages":   []map[string]any{{"role": "user", "content": "ping"}},
		"stream":     true,
		"max_tokens": 64,
	})
	req := &Request{
		Ctx:           context.Background(),
		Config:        &providers.ProviderConfig{BaseURL: "agy-acp://acp/stdio"},
		Provider:      "agy-acp",
		Body:          body,
		IsStream:      true,
		ClaudeClient:  true,
		TranslateResp: true,
		ConnectionID:  "conn-claude-s",
		ConnData:      map[string]any{},
		ACPCredentials: func(ctx context.Context, connData map[string]any, connectionID string) (string, string, time.Time, error) {
			return "access", "refresh", time.Now().Add(time.Hour), nil
		},
	}
	rec := httptest.NewRecorder()
	if err := ForwardAGYACP(rec, req); err != nil {
		t.Fatalf("ForwardAGYACP: %v", err)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q", ct)
	}
	raw := rec.Body.String()
	// Claude SSE lifecycle, not OpenAI chunks: message_start … message_stop,
	// no chat.completion.chunk and no OpenAI [DONE] sentinel.
	for _, want := range []string{"message_start", "content_block_delta", "message_stop"} {
		if !strings.Contains(raw, want) {
			t.Errorf("Claude stream missing %q: %s", want, raw)
		}
	}
	if strings.Contains(raw, "chat.completion.chunk") || strings.Contains(raw, "[DONE]") {
		t.Errorf("Claude stream carried OpenAI framing: %s", raw)
	}
	if !strings.Contains(raw, "PONG") {
		t.Errorf("Claude stream missing answer text: %s", raw)
	}
}

func TestAGYACPRegistered(t *testing.T) {
	if Get("agy-acp") == nil {
		t.Fatal("agy-acp executor not registered")
	}
	_, ok := providers.KnownProviders["agy-acp"]
	if !ok {
		t.Fatal("agy-acp not in provider config")
	}
	if _, ok := agy.ResolveModel("gemini-3.8-flash-high"); !ok {
		t.Fatal("known agy model did not resolve")
	}
}
