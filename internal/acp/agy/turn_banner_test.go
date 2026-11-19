package agy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
 banner:=os.Args[1]
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
   b,_:=json.Marshal(banner)
   fmt.Fprintf(w,"{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"s-1\",\"update\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":%s}}}}\n",b)
   w.Flush()
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
	if out, err := exec.Command(gobin, "build", "-o", path, helper).CombinedOutput(); err != nil {
		t.Fatalf("build fake banner ACP: %v: %s", err, out)
	}
	// Wrap so the banner argument is fixed: RunTurn invokes <bin> acp …
	wrap := filepath.Join(dir, "fakeagy")
	script := "#!/bin/sh\nexec " + path + " " + shellQuote(banner) + "\n"
	if err := os.WriteFile(wrap, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return wrap
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
			fake := fakeBannerACP(t, tt.banner)
			t.Setenv("AGY_PATH", fake)
			var texts []string
			err := RunTurn(context.Background(), TurnOptions{
				AccountRoot: t.TempDir(),
				Credentials: Credentials{AccessToken: "a", RefreshToken: "r"},
			}, "gemini-3.8-flash-medium", []PromptBlock{{Type: "text", Text: "hi"}}, func(ev TurnEvent) {
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
