package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/acp/agy"
	"9router/proxy/internal/providers"
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
	bin := filepath.Join(dir, "fakeagy")
	build := exec.Command("go", "build", "-o", bin, srcPath)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake agy: %v: %s", err, out)
	}
	return bin
}

func TestForwardAGYACPNonStream(t *testing.T) {
	fake := buildFakeAGY(t)
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

func TestForwardAGYACPStream(t *testing.T) {
	fake := buildFakeAGY(t)
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
