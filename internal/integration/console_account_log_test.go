//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"9router/proxy/internal/auth"
	"9router/proxy/internal/db"
)

// newAccountEnv starts a gateway with an empty console buffer, so a case can
// assert on the lines its own requests produced. The buffer is process-wide, so
// clearing it is what separates one case's lines from another's.
func newAccountEnv(t *testing.T) *Env {
	t.Helper()
	env := newEnv(t)
	res := env.Delete(t, "/api/translator/console-logs", WithHeader(auth.CLITokenHeader, auth.CLIToken()))
	if res.Status != http.StatusOK {
		t.Fatalf("console-logs clear status = %d", res.Status)
	}
	return env
}

// A multi-account user has no way to tell which account served a turn: the
// console shows provider+model+tokens and nothing else. Every served request
// must name the account it used.
func TestConsoleLogNamesTheServingAccount(t *testing.T) {
	env := newAccountEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Integration", upstream, "sk-upstream")

	res := env.Post(t, "/v1/chat/completions", ChatBody("ds/deepseek-chat", false))
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.Status, truncate(res.Body))
	}

	usageLine := findLogLine(consoleLogs(t, env), "usage")
	if usageLine == "" {
		t.Fatal("no usage line in console log")
	}
	if !strings.Contains(usageLine, "connName=DeepSeek Integration") {
		t.Errorf("usage line must name the account that served the request, got %q", usageLine)
	}
	if !strings.Contains(usageLine, "conn=conn-deepseek") {
		t.Errorf("usage line must carry the connection id, got %q", usageLine)
	}
}

// Two connections behind one provider: the log has to say which one answered,
// not just that the provider was used.
func TestConsoleLogDistinguishesAccountsInRotation(t *testing.T) {
	env := newAccountEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-a", "deepseek", "Account A", upstream, "sk-a")
	env.AddConnection(t, "conn-b", "deepseek", "Account B", upstream, "sk-b")
	if err := env.Repo.SetProviderStrategy("deepseek", db.ProviderStrategy{RotateStrategy: "round-robin", StickyLimit: 1}); err != nil {
		t.Fatalf("set round-robin strategy: %v", err)
	}

	for range 2 {
		res := env.Post(t, "/v1/chat/completions", ChatBody("ds/deepseek-chat", false))
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d, body %s", res.Status, truncate(res.Body))
		}
	}

	usageLines := findLogLines(consoleLogs(t, env), "usage")
	if len(usageLines) != 2 {
		t.Fatalf("want one usage line per request, got %d: %q", len(usageLines), usageLines)
	}
	served := map[string]bool{}
	for _, line := range usageLines {
		switch {
		case strings.Contains(line, "connName=Account A"):
			served["A"] = true
		case strings.Contains(line, "connName=Account B"):
			served["B"] = true
		default:
			t.Errorf("usage line must name an account, got %q", line)
		}
	}
	if !served["A"] || !served["B"] {
		t.Errorf("rotation served %v across two accounts, want both; lines %q", served, usageLines)
	}
}

func consoleLogs(t *testing.T, env *Env) []string {
	t.Helper()
	res := env.Get(t, "/api/translator/console-logs", WithHeader(auth.CLITokenHeader, auth.CLIToken()))
	if res.Status != http.StatusOK {
		t.Fatalf("console-logs status = %d", res.Status)
	}
	var out struct {
		Success bool     `json:"success"`
		Logs    []string `json:"logs"`
	}
	res.Decode(t, &out)
	if !out.Success {
		t.Fatalf("console-logs not successful: %s", truncate(res.Body))
	}
	return out.Logs
}

func findLogLine(logs []string, substr string) string {
	lines := findLogLines(logs, substr)
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

func findLogLines(logs []string, substr string) []string {
	var out []string
	for _, line := range logs {
		if strings.Contains(line, substr) {
			out = append(out, line)
		}
	}
	return out
}
