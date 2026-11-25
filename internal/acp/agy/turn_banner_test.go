package agy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeBannerACP builds a fake agy that answers session/prompt with a single
// "⚠ …" diagnostic chunk followed by stopReason end_turn — exactly how the
// real agy 1.5.x reports a fatal fault (dead credentials, unusable model).
func fakeBannerACP(t *testing.T, banner string) string {
	t.Helper()
	dir := t.TempDir()
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
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"protocolVersion\":1}}\n",id)
  case "session/new":
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"sessionId\":\"s-1\"}}\n",id)
  case "session/prompt":
   p,_:=m["params"].(map[string]any)
   prompt,_:=p["prompt"].([]any)
   banner:=""
   if len(prompt)>0 { if b,ok:=prompt[0].(map[string]any);ok { banner,_=b["text"].(string) } }
   b,_:=json.Marshal(banner)
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"s-1\",\"update\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":%s}}}}\n",b)
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"stopReason\":\"end_turn\"}}\n",id)
  }
  w.Flush()
 }
}
`
	if err := os.WriteFile(helper, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "fakebanner")
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	// go build appends no extension on Windows, so the exhaustive build in
	// fakeACP produced a bare "fakebanner" that exec could not start.
	binPath := path
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	if out, err := exec.Command(gobin, "build", "-o", binPath, helper).CombinedOutput(); err != nil {
		t.Fatalf("build fake banner ACP: %v: %s", err, out)
	}
	// The banner comes from prompt[0].text, so no shell wrapper is needed.
	return binPath
}

// Regression: agy delivers fatal faults as ordinary message text with
// stopReason end_turn. RunTurn must classify them as errors (so the gateway
// failover loop advances to the next account), never as billed model output.
func TestRunTurnBannerIsError(t *testing.T) {
	tests := []struct {
		name   string
		banner string
		want   error // nil means: still an error, but unclassified (502)
	}{
		{
			name:   "dead credentials surface as executor fault",
			banner: "\n⚠ failed to construct executor: plan model not specified; you must specify a valid model\n",
			want:   nil,
		},
		{
			name:   "sign-in banner is auth",
			banner: "⚠ needs sign-in: run agy to log in",
			want:   ErrAuth,
		},
		{
			name:   "quota banner is quota",
			banner: "⚠ quota exhausted for this account",
			want:   ErrQuota,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := isolatedPoolDeferred(t)
			defer p.Close()
			fake := fakeBannerACP(t, tt.banner)
			t.Setenv("AGY_PATH", fake)
			var texts []string
			err := RunTurn(context.Background(), TurnOptions{
				AccountRoot: t.TempDir(),
				Credentials: Credentials{AccessToken: "a", RefreshToken: "r"},
			}, "gemini-3.8-flash-medium", []PromptBlock{{Type: "text", Text: tt.banner}}, func(ev TurnEvent) {
				if ev.Kind == UpdateText {
					texts = append(texts, ev.Text)
				}
			})
			if err == nil {
				t.Fatalf("banner turn returned nil error; texts=%q", texts)
			}
			if len(texts) != 0 {
				t.Errorf("banner leaked to client as model text: %q", texts)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

// Real model output that merely mentions a warning must still stream: the
// buffer only holds text that could become a lone fatal banner.
func TestRunTurnWarningContentStillStreams(t *testing.T) {
	p := isolatedPoolDeferred(t)
	defer p.Close()
	fake, cleanup := fakeACP(t)
	defer cleanup()
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
	if got := strings.Join(texts, ""); !strings.Contains(got, "Hello") {
		t.Errorf("normal output lost: %q", got)
	}
}

func TestIsAgentDiagnostic(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"⚠ failed to construct executor: plan model not specified", true},
		{"\n⚠ quota exhausted\n", true},
		{"⚠ needs sign-in", true},
		{"PONG", false},
		{"The ⚠ symbol means warning", false},
		{"⚠ this is a long model answer about warning icons that keeps going past the banner shape and contains real prose the user asked for", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isAgentDiagnostic(tt.text); got != tt.want {
			t.Errorf("isAgentDiagnostic(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func newPipeClient(t *testing.T, reply func(id json.RawMessage, prompt string) string) *Client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &Client{
		cmd:      &exec.Cmd{},
		stdin:    inW,
		stdout:   outR,
		stderr:   io.NopCloser(strings.NewReader("")),
		pending:  make(map[string]*rpcRequest),
		notifyCh: make(chan notification, 256),
		done:     make(chan struct{}),
		waitDone: make(chan struct{}),
	}
	close(c.waitDone)

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		defer func() { _ = inR.Close() }()
		defer func() { _ = outW.Close() }()
		scanner := bufio.NewScanner(inR)
		for scanner.Scan() {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					Prompt []struct {
						Text string `json:"text"`
					} `json:"prompt"`
				} `json:"params"`
			}
			if json.Unmarshal(scanner.Bytes(), &req) != nil || req.Method != "session/prompt" {
				continue
			}
			promptText := ""
			if len(req.Params.Prompt) > 0 {
				promptText = req.Params.Prompt[0].Text
			}
			payload := reply(req.ID, promptText)
			if payload != "" {
				if _, err := outW.Write([]byte(payload)); err != nil {
					return
				}
			}
		}
	}()

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		c.readLoop()
	}()

	t.Cleanup(func() {
		c.Close()
		_ = inR.Close()
		<-serverDone
		<-readDone
		<-c.done
	})
	return c
}

func singleChunkAndEndTurn(id json.RawMessage, chunk string) string {
	b, _ := json.Marshal(chunk)
	// Emit several updates before the real chunk and final result in one write so
	// both Notifications() and promptResultCh become ready together in select.
	var sb strings.Builder
	for range 16 {
		sb.WriteString("{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"s-1\",\"update\":{\"sessionUpdate\":\"tool_call_update\"}}}\n")
	}
	fmt.Fprintf(&sb,
		"{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"s-1\",\"update\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":%s}}}}\n{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"stopReason\":\"end_turn\"}}\n",
		b, id,
	)
	return sb.String()
}

func TestStreamPromptDrainsQueuedNotifications(t *testing.T) {
	t.Run("banner_error_never_returns_nil", func(t *testing.T) {
		c := newPipeClient(t, func(id json.RawMessage, _ string) string {
			return singleChunkAndEndTurn(id, "⚠ needs sign-in: run agy to log in")
		})
		for i := range 30 {
			var texts []string
			err := streamPromptOnce(context.Background(), c, "s-1", []PromptBlock{{Type: "text", Text: "hi"}}, nil, func(ev TurnEvent) {
				if ev.Kind == UpdateText {
					texts = append(texts, ev.Text)
				}
			})
			if !errors.Is(err, ErrAuth) {
				t.Fatalf("iter %d: error = %v, want ErrAuth", i, err)
			}
			if len(texts) != 0 {
				t.Fatalf("iter %d: leaked banner text %q", i, texts)
			}
		}
	})

	t.Run("deferred_status_always_triggers_stall", func(t *testing.T) {
		c := newPipeClient(t, func(id json.RawMessage, _ string) string {
			return singleChunkAndEndTurn(id, "Checking model and configuration in the background...")
		})
		for i := range 30 {
			err := streamPrompt(context.Background(), c, "s-1", []PromptBlock{{Type: "text", Text: "hi"}}, nil, func(TurnEvent) {})
			if !errors.Is(err, ErrStall) {
				t.Fatalf("iter %d: error = %v, want ErrStall", i, err)
			}
		}
	})

	t.Run("streamed_text_is_not_dropped_or_leaked_across_pooled_turns", func(t *testing.T) {
		c := newPipeClient(t, func(id json.RawMessage, prompt string) string {
			return singleChunkAndEndTurn(id, prompt)
		})
		for i := range 30 {
			want := fmt.Sprintf("turn-%d", i)
			var got strings.Builder
			err := streamPromptOnce(context.Background(), c, "s-1", []PromptBlock{{Type: "text", Text: want}}, nil, func(ev TurnEvent) {
				if ev.Kind == UpdateText {
					got.WriteString(ev.Text)
				}
			})
			if err != nil {
				t.Fatalf("iter %d: unexpected error: %v", i, err)
			}
			if got.String() != want {
				t.Fatalf("iter %d: got text %q, want %q", i, got.String(), want)
			}
			if leftover := len(c.Notifications()); leftover != 0 {
				t.Fatalf("iter %d: %d unread notifications left in queue", i, leftover)
			}
		}
	})
}
