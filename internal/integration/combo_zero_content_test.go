//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

const zeroContentJSON = `{
  "id": "chatcmpl-zero",
  "object": "chat.completion",
  "created": 1700000000,
  "model": "deepseek-chat",
  "choices": [
    {"index": 0, "message": {"role": "assistant", "content": ""}, "finish_reason": "in_progress"}
  ]
}`

// TestComboFallsBackOnZeroContentResponse verifies that when a combo member
// returns HTTP 200 with zero usable content, the combo falls back to the next
// member and serves that member's answer.
func TestComboFallsBackOnZeroContentResponse(t *testing.T) {
	env := newEnv(t)

	zeroUpstream := env.NewUpstream(t, JSONResponder(http.StatusOK, zeroContentJSON))
	workingUpstream := env.NewUpstream(t, chatCompletionResponder())

	env.AddConnection(t, "conn-zero", "deepseek", "Zero Content Account", zeroUpstream, "sk-zero")
	env.AddConnection(t, "conn-working", "groq", "Working Account", workingUpstream, "sk-working")
	env.AddCombo(t, "combo-zero", "resilient-zero", []string{"deepseek/deepseek-chat", "groq/llama-3.3-70b-versatile"})

	res := env.Post(t, "/v1/chat/completions", ChatBody("resilient-zero", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions with combo = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := zeroUpstream.Count(); got != 1 {
		t.Errorf("zero-content upstream received %d requests, want 1", got)
	}
	if got := workingUpstream.Count(); got != 1 {
		t.Errorf("working upstream received %d requests, want 1", got)
	}
	if !strings.Contains(string(res.Body), "upstream reply") {
		t.Errorf("response body = %q, want working upstream reply", string(res.Body))
	}

	// Verify the zero-content connection was NOT put into cooldown.
	conn, err := env.Repo.GetProviderConnectionByID("conn-zero")
	if err != nil {
		t.Fatalf("get connection: %v", err)
	}
	if until, ok := db.ConnectionCooldownUntil(conn.Data); ok {
		t.Errorf("conn-zero should not have rateLimitedUntil set, got: %v", until)
	}
	if locked, _ := env.Repo.IsConnectionModelLocked("conn-zero", "deepseek-chat"); locked {
		t.Error("conn-zero should not have modelLock set")
	}

	// Verify usage accounting (FR6/T4): discarded zero-content attempt must not produce a success usage row.
	rows, err := env.Repo.GetRecentUsageHistory(10)
	if err != nil {
		t.Fatalf("read usage history: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("usageHistory rows = %d, want exactly 1 for the served turn", len(rows))
	}
	if rows[0].Provider != "groq" || rows[0].ConnectionID != "conn-working" {
		t.Errorf("usageHistory row billed provider %q / conn %q, want groq / conn-working", rows[0].Provider, rows[0].ConnectionID)
	}
}

// TestComboSingleMemberZeroContentServedVerbatim verifies that when the only
// (last) member of a combo returns HTTP 200 with zero usable content, that 200
// is served verbatim to the client rather than converted into an error.
func TestComboSingleMemberZeroContentServedVerbatim(t *testing.T) {
	env := newEnv(t)

	zeroUpstream := env.NewUpstream(t, JSONResponder(http.StatusOK, zeroContentJSON))
	env.AddConnection(t, "conn-solo-zero", "deepseek", "Solo Zero Account", zeroUpstream, "sk-solo-zero")
	env.AddCombo(t, "combo-solo-zero", "solo-zero", []string{"deepseek/deepseek-chat"})

	res := env.Post(t, "/v1/chat/completions", ChatBody("solo-zero", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions with single-member combo = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if !strings.Contains(string(res.Body), "in_progress") {
		t.Errorf("response body = %q, want original zero-content body", string(res.Body))
	}

	rows, err := env.Repo.GetRecentUsageHistory(10)
	if err != nil {
		t.Fatalf("read usage history: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("usageHistory rows = %d, want exactly 1 for the served turn", len(rows))
	}

	details, total, err := env.Repo.GetRequestDetailsPaged(context.Background(), 10, 0, "")
	if err != nil {
		t.Fatalf("read request details: %v", err)
	}
	if total == 0 || len(details) == 0 {
		t.Fatal("requestDetails returned no records")
	}
	if details[0].Status != "success" {
		t.Errorf("requestDetail[0].Status = %q, want success", details[0].Status)
	}
}

// TestMessagesComboFallsBackOnZeroContentResponse verifies that combo fallback
// on zero usable content works for /v1/messages as well.
func TestMessagesComboFallsBackOnZeroContentResponse(t *testing.T) {
	env := newEnv(t)

	zeroUpstream := env.NewUpstream(t, JSONResponder(http.StatusOK, zeroContentJSON))
	workingUpstream := env.NewUpstream(t, chatCompletionResponder())

	env.AddConnection(t, "conn-msg-zero", "deepseek", "Zero Content Account", zeroUpstream, "sk-zero")
	env.AddConnection(t, "conn-msg-working", "groq", "Working Account", workingUpstream, "sk-working")
	env.AddCombo(t, "combo-msg-zero", "msg-resilient-zero", []string{"deepseek/deepseek-chat", "groq/llama-3.3-70b-versatile"})

	res := env.Post(t, "/v1/messages", claudeMessagesBody("msg-resilient-zero", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/messages with combo = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := zeroUpstream.Count(); got != 1 {
		t.Errorf("zero-content upstream received %d requests, want 1", got)
	}
	if got := workingUpstream.Count(); got != 1 {
		t.Errorf("working upstream received %d requests, want 1", got)
	}
	if !strings.Contains(string(res.Body), "upstream reply") {
		t.Errorf("response body = %q, want working upstream reply", string(res.Body))
	}
}

// TestComboFallsBackOnNumericFinishReason verifies that a combo advances when
// the first member answers with a numeric finish_reason.
func TestComboFallsBackOnNumericFinishReason(t *testing.T) {
	env := newEnv(t)

	numericFinishJSON := `{
	  "id": "chatcmpl-num-finish",
	  "object": "chat.completion",
	  "created": 1700000000,
	  "model": "deepseek-chat",
	  "choices": [
	    {"index": 0, "message": {"role": "assistant", "content": ""}, "finish_reason": 5}
	  ]
	}`

	zeroUpstream := env.NewUpstream(t, JSONResponder(http.StatusOK, numericFinishJSON))
	workingUpstream := env.NewUpstream(t, chatCompletionResponder())

	env.AddConnection(t, "conn-num-zero", "deepseek", "Zero Content Account", zeroUpstream, "sk-zero")
	env.AddConnection(t, "conn-num-working", "groq", "Working Account", workingUpstream, "sk-working")
	env.AddCombo(t, "combo-num-zero", "resilient-num-zero", []string{"deepseek/deepseek-chat", "groq/llama-3.3-70b-versatile"})

	res := env.Post(t, "/v1/chat/completions", ChatBody("resilient-num-zero", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := zeroUpstream.Count(); got != 1 {
		t.Errorf("zero-content upstream received %d requests, want 1", got)
	}
	if got := workingUpstream.Count(); got != 1 {
		t.Errorf("working upstream received %d requests, want 1", got)
	}
	if !strings.Contains(string(res.Body), "upstream reply") {
		t.Errorf("response body = %q, want working upstream reply", string(res.Body))
	}
}

// TestComboFallsBackOnArrayReasoningContent verifies that a combo advances when
// the first member answers with reasoning_content as an array.
func TestComboFallsBackOnArrayReasoningContent(t *testing.T) {
	env := newEnv(t)

	arrayReasoningJSON := `{
	  "id": "chatcmpl-arr-reason",
	  "object": "chat.completion",
	  "created": 1700000000,
	  "model": "deepseek-chat",
	  "choices": [
	    {"index": 0, "message": {"role": "assistant", "content": "", "reasoning_content": [{"type":"text","text":"thinking"}]}, "finish_reason": "in_progress"}
	  ]
	}`

	zeroUpstream := env.NewUpstream(t, JSONResponder(http.StatusOK, arrayReasoningJSON))
	workingUpstream := env.NewUpstream(t, chatCompletionResponder())

	env.AddConnection(t, "conn-arr-zero", "deepseek", "Zero Content Account", zeroUpstream, "sk-zero")
	env.AddConnection(t, "conn-arr-working", "groq", "Working Account", workingUpstream, "sk-working")
	env.AddCombo(t, "combo-arr-zero", "resilient-arr-zero", []string{"deepseek/deepseek-chat", "groq/llama-3.3-70b-versatile"})

	res := env.Post(t, "/v1/chat/completions", ChatBody("resilient-arr-zero", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := zeroUpstream.Count(); got != 1 {
		t.Errorf("zero-content upstream received %d requests, want 1", got)
	}
	if got := workingUpstream.Count(); got != 1 {
		t.Errorf("working upstream received %d requests, want 1", got)
	}
	if !strings.Contains(string(res.Body), "upstream reply") {
		t.Errorf("response body = %q, want working upstream reply", string(res.Body))
	}
}

// TestMessagesComboLastMemberZeroContentInClaudeShape verifies that when the
// last member of a combo returns zero content on /v1/messages, the answer is
// served in the client's wire shape (Anthropic messages), is recorded in usageHistory,
// and has requestDetail status = "success".
func TestMessagesComboLastMemberZeroContentInClaudeShape(t *testing.T) {
	env := newEnv(t)

	zeroUpstream := env.NewUpstream(t, JSONResponder(http.StatusOK, zeroContentJSON))
	env.AddConnection(t, "conn-last-claude", "deepseek", "Last Claude Account", zeroUpstream, "sk-last-claude")
	env.AddCombo(t, "combo-last-claude", "solo-claude", []string{"deepseek/deepseek-chat"})

	res := env.Post(t, "/v1/messages", claudeMessagesBody("solo-claude", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/messages = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	bodyStr := string(res.Body)
	if !strings.Contains(bodyStr, `"type":"message"`) && !strings.Contains(bodyStr, `"type": "message"`) {
		t.Errorf("response body = %q, want Claude wire shape with type: message", bodyStr)
	}
	if strings.Contains(bodyStr, `"choices"`) || strings.Contains(bodyStr, `"chat.completion"`) {
		t.Errorf("response body = %q, must not contain OpenAI choices or chat.completion", bodyStr)
	}

	// Verify usage and status recording (Findings 2 & 3).
	rows, err := env.Repo.GetRecentUsageHistory(10)
	if err != nil {
		t.Fatalf("read usage history: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("usageHistory rows = %d, want exactly 1 for the served turn", len(rows))
	}

	details, total, err := env.Repo.GetRequestDetailsPaged(context.Background(), 10, 0, "")
	if err != nil {
		t.Fatalf("read request details: %v", err)
	}
	if total == 0 || len(details) == 0 {
		t.Fatal("requestDetails returned no records")
	}
	if details[0].Status != "success" {
		t.Errorf("requestDetail[0].Status = %q, want success", details[0].Status)
	}
}
