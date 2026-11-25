package agy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// cancelResultWriter responds to session/prompt with a complete answer inside
// the stdin write itself. That makes the prompt result ready before the pump
// starts, so a caller that cancels from an emitted event exercises the race
// where cancellation lands while the result is being delivered — not the
// simpler ctx.Done branch.
type cancelResultWriter struct {
	client *Client
}

func (w *cancelResultWriter) Write(p []byte) (int, error) {
	var msg rpcMessage
	if err := json.Unmarshal(p, &msg); err == nil && msg.Method == "session/prompt" {
		response, _ := json.Marshal(rpcMessage{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Result:  json.RawMessage(`{"stopReason":"end_turn","content":[{"type":"text","text":"answer"}]}`),
		})
		w.client.dispatch(response)
	}
	return len(p), nil
}

func (*cancelResultWriter) Close() error { return nil }

func TestStreamPromptCancelledWhileEmittingResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := &cancelResultWriter{}
	client := &Client{
		stdin:    writer,
		pending:  make(map[string]*rpcRequest),
		notifyCh: make(chan notification, 4),
		done:     make(chan struct{}),
	}
	writer.client = client
	err := streamPromptOnce(ctx, client, "s-1", []PromptBlock{{Type: "text", Text: "hi"}}, nil,
		func(ev TurnEvent) {
			if ev.Kind == UpdateText {
				cancel()
			}
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled during result emission: got %v, want context.Canceled", err)
	}
}

// poolACP extends the fake ACP helper with a per-process call counter so a
// test can tell that a second turn reused the worker/session instead of
// re-spawning. It reports counts via a JSON-RPC notification that the client
// ignores, but the counter file persists across calls within one process.
func poolACP(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepathJoin(dir, "poolfake"+exeSuffix)
	src := `package main
import ("bufio";"encoding/json";"fmt";"os";"sync/atomic")
var n atomic.Int64
func main(){
 r:=bufio.NewReader(os.Stdin); w:=bufio.NewWriter(os.Stdout); defer w.Flush()
 for { line,err:=r.ReadString('\n'); if err!=nil{return}
  var m map[string]any; if json.Unmarshal([]byte(line),&m)!=nil{continue}
  method,_:=m["method"].(string); id:=m["id"]
  switch method {
  case "initialize":
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"protocolVersion\":1}}\n",id)
  case "session/new":
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"sessionId\":\"s-%d\",\"models\":{\"currentModelId\":\"m\"}}}\n",id,n.Add(1))
  case "session/prompt":
   p,_:=m["params"].(map[string]any)
   sid,_:=p["sessionId"].(string)
   b,_:=json.Marshal("OK:"+sid)
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":%q,"+
     "\"update\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":%s}}}}\n",sid,b)
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"stopReason\":\"end_turn\"}}\n",id)
  }
  w.Flush()
 }
}
`
	if err := writeTestFile(filepathJoin(dir, "helper.go"), src); err != nil {
		t.Fatal(err)
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(goBin, "build", "-o", path, filepath.Join(dir, "helper.go")).CombinedOutput(); err != nil {
		t.Fatalf("build pool fake: %v: %s", err, out)
	}
	return path
}

// TestPoolReusesWorkerAndSession proves the fast path: after a first turn the
// worker stays alive and a prepared session is ready, so the second turn's
// session id comes from the refill and no new initialize is needed.
func TestPoolReusesWorkerAndSession(t *testing.T) {
	p := isolatedPoolDeferred(t)
	defer p.Close()
	fake := poolACP(t)
	t.Setenv("AGY_PATH", fake)
	root := t.TempDir()
	creds := Credentials{AccessToken: "a", RefreshToken: "r"}
	opts := TurnOptions{AccountRoot: root, Credentials: creds}
	turn := func() string {
		t.Helper()
		var text string
		if err := RunTurn(context.Background(), opts, "gemini-3.8-flash-high",
			[]PromptBlock{{Type: "text", Text: "hi"}}, func(ev TurnEvent) {
				if ev.Kind == UpdateText {
					text += ev.Text
				}
			}); err != nil {
			t.Fatal(err)
		}
		return text
	}
	first := turn()
	// Wait for the background refill to prepare the next session.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		ready := false
		for _, e := range p.entries {
			ready = ready || (e.sid != "" && !e.inUse)
		}
		p.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	second := turn()
	if first == "" || second == "" {
		t.Fatalf("empty turn output: first=%q second=%q", first, second)
	}
	if first == second {
		t.Errorf("second turn reused same session %q; want a refilled one", first)
	}
	p.mu.Lock()
	workers := len(p.entries)
	p.mu.Unlock()
	if workers != 1 {
		t.Errorf("pool workers = %d, want 1 (worker was not reused)", workers)
	}
}

// TestPoolRotatedCredentialsDiscardsWorker pins that a refreshed token is
// written before the next turn: the worker keyed on the old pair is replaced.
func TestPoolRotatedCredentialsDiscardsWorker(t *testing.T) {
	p := isolatedPoolDeferred(t)
	defer p.Close()
	fake := poolACP(t)
	t.Setenv("AGY_PATH", fake)
	root := t.TempDir()
	firstCreds := Credentials{AccessToken: "a", RefreshToken: "r"}
	if err := RunTurn(context.Background(), TurnOptions{AccountRoot: root, Credentials: firstCreds},
		"gemini-3.8-flash-high", []PromptBlock{{Type: "text", Text: "hi"}}, func(TurnEvent) {}); err != nil {
		t.Fatal(err)
	}
	secondCreds := Credentials{AccessToken: "b", RefreshToken: "r2"}
	if err := RunTurn(context.Background(), TurnOptions{AccountRoot: root, Credentials: secondCreds},
		"gemini-3.8-flash-high", []PromptBlock{{Type: "text", Text: "hi"}}, func(TurnEvent) {}); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	var cred string
	for _, e := range p.entries {
		cred = e.credKey
	}
	p.mu.Unlock()
	if cred != credKey(secondCreds) {
		t.Errorf("pool credential = %q, want %q", cred, credKey(secondCreds))
	}
}

// TestPoolDisabledFallsBackToColdPath checks the AGY_ACP_POOL=0 kill switch.
func TestPoolDisabledFallsBackToColdPath(t *testing.T) {
	p := isolatedPoolDeferred(t)
	defer p.Close()
	fake := poolACP(t)
	t.Setenv("AGY_PATH", fake)
	t.Setenv("AGY_ACP_POOL", "0")
	if err := RunTurn(context.Background(), TurnOptions{
		AccountRoot: t.TempDir(), Credentials: Credentials{AccessToken: "a", RefreshToken: "r"},
	}, "gemini-3.8-flash-high", []PromptBlock{{Type: "text", Text: "hi"}}, func(TurnEvent) {}); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	workers := len(p.entries)
	p.mu.Unlock()
	if workers != 0 {
		t.Errorf("pool workers = %d with AGY_ACP_POOL=0, want 0", workers)
	}
}

// TestPoolCancelledTurnKeepsWorker proves session/cancel keeps the pooled
// worker reusable instead of killing it on client disconnect. Cancellation is
// triggered from the first streamed update so the worker is already pooled and
// the cancel lands mid-prompt (not before spawn, which now aborts early).
func TestPoolCancelledTurnKeepsWorker(t *testing.T) {
	p := isolatedPoolDeferred(t)
	defer p.Close()
	fake := poolACP(t)
	t.Setenv("AGY_PATH", fake)
	root := t.TempDir()
	opts := TurnOptions{AccountRoot: root, Credentials: Credentials{AccessToken: "a", RefreshToken: "r"}}
	if err := RunTurn(context.Background(), opts, "gemini-3.8-flash-high", []PromptBlock{{Type: "text", Text: "hi"}}, func(TurnEvent) {}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := RunTurn(ctx, opts, "gemini-3.8-flash-high", []PromptBlock{{Type: "text", Text: "hi"}}, func(ev TurnEvent) {
		if ev.Kind == UpdateText {
			cancel()
		}
	})
	if err == nil {
		t.Fatal("cancelled turn returned nil error")
	}
	p.mu.Lock()
	workers := len(p.entries)
	p.mu.Unlock()
	if workers == 0 {
		t.Error("cancelled turn discarded its pooled worker; want it retained")
	}
}

// statusACP answers the first session/prompt with only a deferred-status
// line, then a real answer on the next (nudge) prompt.
func statusACP(t *testing.T, alwaysStatus bool) string {
	t.Helper()
	dir := t.TempDir()
	path := filepathJoin(dir, "statusfake"+exeSuffix)
	src := `package main
import ("bufio";"encoding/json";"fmt";"os";"sync/atomic")
var n atomic.Int64
func main(){
 always:=len(os.Args)>1 && os.Args[1]=="always"
 r:=bufio.NewReader(os.Stdin); w:=bufio.NewWriter(os.Stdout); defer w.Flush()
 for { line,err:=r.ReadString('\n'); if err!=nil{return}
  var m map[string]any; if json.Unmarshal([]byte(line),&m)!=nil{continue}
  method,_:=m["method"].(string); id:=m["id"]
  switch method {
  case "initialize":
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"protocolVersion\":1}}\n",id)
  case "session/new":
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"sessionId\":\"s-1\"}}\n",id)
  case "session/prompt":
   text:="The answer is 42."
   if n.Add(1)==1 || always { text="Checking model and configuration in the background..." }
   b,_:=json.Marshal(text)
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":%s}}}\n",b)
   w.Flush()
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"stopReason\":\"end_turn\"}}\n",id)
  }
  w.Flush()
 }
}
`
	if err := writeTestFile(filepathJoin(dir, "helper.go"), src); err != nil {
		t.Fatal(err)
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	buildArgs := []string{"build", "-o", path, filepathJoin(dir, "helper.go")}
	if out, err := exec.Command(goBin, buildArgs...).CombinedOutput(); err != nil {
		t.Fatalf("build status fake: %v: %s", err, out)
	}
	// The fake reads os.Args[1]; wrap it so "always" is passed on Windows
	// (exec.Command cannot run a shell script directly).
	if alwaysStatus {
		if runtime.GOOS == "windows" {
			wrap := filepathJoin(dir, "always.bat")
			if err := os.WriteFile(wrap, []byte("@echo off\r\n\""+path+"\" always\r\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			return wrap
		}
		wrap := filepathJoin(dir, "always.sh")
		if err := os.WriteFile(wrap, []byte("#!/bin/sh\nexec "+path+" always\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		return wrap
	}
	return path
}

// TestStatusOnlyTurnNudgesForAnswer proves the reported bug is fixed: a lone
// "checking model/config in the background" line no longer ends the turn.
func TestStatusOnlyTurnNudgesForAnswer(t *testing.T) {
	p := isolatedPoolDeferred(t)
	defer p.Close()
	fake := statusACP(t, false)
	t.Setenv("AGY_PATH", fake)
	var text string
	err := RunTurn(context.Background(), TurnOptions{
		AccountRoot: t.TempDir(), Credentials: Credentials{AccessToken: "a", RefreshToken: "r"},
	}, "gemini-3.8-flash-high", []PromptBlock{{Type: "text", Text: "what is the answer?"}}, func(ev TurnEvent) {
		if ev.Kind == UpdateText {
			text += ev.Text
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "answer is 42") {
		t.Errorf("turn did not continue after status line; text=%q", text)
	}
	if strings.Contains(text, "Checking model") {
		t.Errorf("status line leaked to client: %q", text)
	}
}

// TestStatusOnlyTurnEventuallyFails proves repeated deferral is surfaced as
// ErrStall instead of an empty HTTP 200.
func TestStatusOnlyTurnEventuallyFails(t *testing.T) {
	p := isolatedPoolDeferred(t)
	defer p.Close()
	fake := statusACP(t, true)
	t.Setenv("AGY_PATH", fake)
	err := RunTurn(context.Background(), TurnOptions{
		AccountRoot: t.TempDir(), Credentials: Credentials{AccessToken: "a", RefreshToken: "r"},
	}, "gemini-3.8-flash-high", []PromptBlock{{Type: "text", Text: "hi"}}, func(TurnEvent) {})
	if !errors.Is(err, ErrStall) {
		t.Errorf("error = %v, want ErrStall", err)
	}
}

func TestIsDeferredStatus(t *testing.T) {
	for _, tt := range []struct {
		text string
		want bool
	}{
		{"Checking model and configuration in the background...", true},
		{"Checking model and config in background", true},
		{"Cek model dan konfigurasi sedang berjalan di latar belakang...", true},
		{"The answer is 42", false},
		{"", false},
	} {
		if got := isDeferredStatus(tt.text); got != tt.want {
			t.Errorf("isDeferredStatus(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func filepathJoin(dir, name string) string     { return filepath.Join(dir, name) }
func writeTestFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }

// TestSpawnRespectsCancelledCallerContext pins that spawn aborts when the
// caller's context is already cancelled, without spawning a worker that would
// outlive the turn. Handshake must honour ctx even though the process itself
// is intentionally started on a background context (so it can be pooled).
func TestSpawnRespectsCancelledCallerContext(t *testing.T) {
	p := NewPool(1, time.Minute)
	defer p.Close()
	fake := poolACP(t)
	t.Setenv("AGY_PATH", fake)
	root := t.TempDir()
	dir, err := NewAccountDir(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = p.spawn(ctx, &poolEntry{dir: dir}, TurnOptions{}, fake)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("spawn error = %v, want context.Canceled", err)
	}
}

// TestPoolCloseClearsCredentialsAndWorker pins the shutdown contract used by
// the gateway's OnStop hook: Close kills the worker and clears its on-disk
// token files, so a clean exit leaves no credential material behind.
func TestPoolCloseClearsCredentialsAndWorker(t *testing.T) {
	p := NewPool(1, time.Minute)
	fake := poolACP(t)
	t.Setenv("AGY_PATH", fake)
	root := t.TempDir()
	dir, err := NewAccountDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := dir.WriteCredentials(Credentials{AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(root, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if _, err := os.Stat(tokenFile); err != nil {
		t.Fatalf("token file not seeded: %v", err)
	}
	e := &poolEntry{dir: dir}
	if err := p.spawn(context.Background(), e, TurnOptions{}, fake); err != nil {
		t.Fatal(err)
	}
	p.entries[e.key] = e
	if e.client == nil {
		t.Fatal("worker not started")
	}
	p.Close()
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		t.Errorf("token file survived Close: %v", err)
	}
	if len(p.entries) != 0 {
		t.Errorf("pool entries after Close = %d, want 0", len(p.entries))
	}
}
