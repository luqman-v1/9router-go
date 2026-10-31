//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

// commandCodeNDJSONResponder answers with the AI SDK v5 NDJSON event stream
// Command Code serves on /alpha/generate: a text delta, then a finish event
// whose totalUsage carries input/output tokens plus cached reads and
// reasoning tokens.
func commandCodeNDJSONResponder() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-command-code-version"); got != "1.88.0" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"success":false,"error":{"code":"FORBIDDEN","status":403,"message":"bad identity headers"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(
			`{"type":"text-delta","text":"cc hello"}` + "\n" +
				`{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":100,"outputTokens":5,"totalTokens":105,"cachedInputTokens":64,"reasoningTokens":2}}` + "\n"))
	}
}

// TestCommandcodeUsageAccountingRecordsRealTokens proves the executor's usage
// mapping reaches usageHistory through the real router: a commandcode
// connection to a fake upstream serving NDJSON with cachedInputTokens must
// record the upstream counts (not the OutputLength/4 estimate) in the
// usageHistory row, including its cached/reasoning details.
func TestCommandcodeUsageAccountingRecordsRealTokens(t *testing.T) {
	env := newEnv(t)

	upstream := env.NewUpstream(t, commandCodeNDJSONResponder())
	env.AddConnection(t, "conn-cmc", "commandcode", "CommandCode Integration", upstream, "user_test")

	res := env.Post(t, "/v1/chat/completions", ChatBody("commandcode/deepseek/deepseek-v4.1-flash", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if !strings.Contains(string(res.Body), `"cached_tokens":64`) {
		t.Errorf("client chunk missing cached_tokens=64, body: %s", truncate(res.Body))
	}

	rows, err := env.Repo.GetRecentUsageHistory(10)
	if err != nil {
		t.Fatalf("read usage history: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("usageHistory rows = %d, want exactly 1", len(rows))
	}
	row := rows[0]
	if row.Provider != "commandcode" || row.ConnectionID != "conn-cmc" {
		t.Errorf("row billed provider %q / conn %q, want commandcode / conn-cmc", row.Provider, row.ConnectionID)
	}
	if row.PromptTokens != 100 || row.CompletionTokens != 5 {
		t.Errorf("row tokens prompt=%d completion=%d, want upstream 100/5", row.PromptTokens, row.CompletionTokens)
	}
	if !strings.Contains(row.Tokens, `"cached_tokens":64`) {
		t.Errorf("row tokens %q missing cached_tokens=64", row.Tokens)
	}
	if !strings.Contains(row.Tokens, `"reasoning_tokens":2`) {
		t.Errorf("row tokens %q missing reasoning_tokens=2", row.Tokens)
	}
}
