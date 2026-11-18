package agy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeACP is a tiny scripted ACP server used as a stand-in for the agy binary.
// It speaks NDJSON on stdin/stdout and drives the client through the full
// initialize -> session/new -> session/prompt handshake.
func fakeACP(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fakeagy")
	// The script is passed via a compiled helper binary to avoid shell quoting.
	helper := filepath.Join(dir, "helper.go")
	src := `package main
import ("bufio";"encoding/json";"fmt";"os")
func main(){
 r:=bufio.NewReader(os.Stdin); w:=bufio.NewWriter(os.Stdout); defer w.Flush()
 for { line,err:=r.ReadString('\n'); if err!=nil{return}
  var m map[string]any; if json.Unmarshal([]byte(line),&m)!=nil{continue}
  method,_:=m["method"].(string)
  id:=m["id"]
  switch method {
  case "initialize":
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"protocolVersion\":1,\"agentInfo\":{\"name\":\"fake\",\"version\":\"1\"}}}\n",id)
  case "session/new":
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"sessionId\":\"s-1\",\"models\":{\"availableModels\":[]}}}\n",id)
  case "session/prompt":
   // stream two text chunks then finish
   fmt.Fprintln(w,"{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"s-1\",\"update\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"Hello \"}}}}")
   w.Flush()
   fmt.Fprintln(w,"{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"s-1\",\"update\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"world\"}}}}")
   w.Flush()
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"stopReason\":\"end_turn\",\"content\":[{\"type\":\"text\",\"text\":\"Hello world\"}]}}\n",id)
  }
  w.Flush()
 }
}
`
	if err := os.WriteFile(helper, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	bin, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command(bin, "build", "-o", path, helper)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake ACP: %v: %s", err, out)
	}
	return path, func() {}
}

func TestResolveModel(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"gemini-3.8-flash-high", "gemini-3.8-flash-high", true},
		{"claude-sonnet-4-6", "claude-sonnet-4-6", true},
		{"gemini-3.8-flash", "gemini-3.8-flash-medium", true},
		{"totally-unknown", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := ResolveModel(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ResolveModel(%q) = %q,%v; want %q,%v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestBuildPrompt(t *testing.T) {
	req := &ChatRequest{Messages: []ChatMessage{
		{Role: "system", Content: json.RawMessage(`"be terse"`)},
		{Role: "user", Content: json.RawMessage(`"hi"`)},
		{Role: "tool", ToolCallID: "call_1", Content: json.RawMessage(`"result"`)},
	}}
	blocks := BuildPrompt(req)
	if len(blocks) != 1 {
		t.Fatalf("want 1 combined block, got %d", len(blocks))
	}
	text := blocks[0].Text
	for _, want := range []string{"[System]", "be terse", "[User]", "hi", "[Tool result id=call_1]", "result"} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt missing %q: %s", want, text)
		}
	}
}

func TestParseUpdate(t *testing.T) {
	raw := json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"delta"}}`)
	kind, text, _, err := ParseUpdate(raw)
	if err != nil || kind != UpdateText || text != "delta" {
		t.Fatalf("ParseUpdate = %v,%q,%v", kind, text, err)
	}
}

func TestWriteCredentials(t *testing.T) {
	root := t.TempDir()
	dir, err := NewAccountDir(filepath.Join(root, "acct"))
	if err != nil {
		t.Fatal(err)
	}
	err = dir.WriteCredentials(Credentials{
		AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir.Root, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("credential mode = %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Token struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"token"`
		AuthMethod string `json:"auth_method"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Token.AccessToken != "a" || payload.Token.RefreshToken != "r" || payload.AuthMethod != "consumer" {
		t.Errorf("credential payload wrong: %s", data)
	}
	if err := dir.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("credential file not cleared: %v", err)
	}
}

func TestClientCall(t *testing.T) {
	fake, cleanup := fakeACP(t)
	defer cleanup()
	// Use the client directly with the fake binary.
	cmd := exec.Command(fake)
	c, err := Start(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := c.Call(context.Background(), "initialize", map[string]any{"protocolVersion": 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res), "fake") {
		t.Errorf("unexpected init result: %s", res)
	}
}

func TestRunTurnStream(t *testing.T) {
	fake, cleanup := fakeACP(t)
	defer cleanup()
	// Force the fake binary through AGY_PATH.
	t.Setenv("AGY_PATH", fake)
	var texts []string
	err := RunTurn(context.Background(), TurnOptions{
		AccountRoot: t.TempDir(),
		Credentials: Credentials{AccessToken: "a", RefreshToken: "r"},
	}, "gemini-3.8-flash-high", []PromptBlock{{Type: "text", Text: "hi"}}, func(ev TurnEvent) {
		if ev.Kind == UpdateText {
			texts = append(texts, ev.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(texts, "")
	// The fake sends both streamed chunks and a final result; the exact
	// interleaving is scheduler-dependent. Assert both pieces arrived.
	if !strings.Contains(got, "Hello") || !strings.Contains(got, "world") {
		t.Errorf("streamed text = %q", got)
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		err  error
		want error
	}{
		{fmt.Errorf("401 unauthorized"), ErrAuth},
		{fmt.Errorf("quota exhausted 429"), ErrQuota},
		{fmt.Errorf("account disabled policy"), ErrPolicy},
	}
	for _, tt := range tests {
		got := classifyError(tt.err)
		if !strings.Contains(got.Error(), tt.want.Error()) {
			t.Errorf("classifyError(%v) = %v, want contains %v", tt.err, got, tt.want)
		}
	}
}
