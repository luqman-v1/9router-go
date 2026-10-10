//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	json "encoding/json/v2"
)

// addMCodeConnection stores a minimax-code connection aimed at the fake
// gateway. The base URL ends in /messages so getProviderConfig passes it
// through untouched (it only completes a URL that names no route), which is
// how the mavis endpoint is reached in a test.
func addMCodeConnection(t *testing.T, env *Env, id string, up *Upstream) {
	t.Helper()
	priority := 1
	data := `{"apiKey":"mcode-access","baseUrl":"` + up.URL + `/mavis/api/v1/llm/v1/messages"}`
	if err := env.Repo.CreateProviderConnectionFull(id, "minimax-code", "oauth", "MiniMax Code", &priority, data); err != nil {
		t.Fatalf("create connection %s: %v", id, err)
	}
	stored, err := env.Repo.GetProviderConnectionByID(id)
	if err != nil || stored == nil {
		t.Fatalf("read back connection %s: %v", id, err)
	}
	if !strings.Contains(stored.Data, up.URL) {
		t.Fatalf("connection %s did not persist the fake upstream URL: %s", id, stored.Data)
	}
}

// claudeMessagesResponseJSON is a well-formed Anthropic Messages reply, so a
// passing case exercises the real translation path rather than an error path.
const claudeMessagesResponseJSON = `{
  "id": "msg_integration",
  "type": "message",
  "role": "assistant",
  "model": "MiniMax-M3",
  "content": [{"type": "text", "text": "upstream reply"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 11, "output_tokens": 7}
}`

// TestMiniMaxCodeClaudeWire pins the part a unit test cannot see: that a
// /v1/messages client reaches the mavis endpoint untranslated, with the OAuth
// bearer and the per-request headers the gateway requires, and that the Claude
// reply comes back in the shape the client asked for.
func TestMiniMaxCodeClaudeWire(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, JSONResponder(http.StatusOK, claudeMessagesResponseJSON))
	addMCodeConnection(t, env, "conn-mcode", upstream)

	res := env.Post(t, "/v1/messages", claudeMessagesBody("minimax-code/MiniMax-M3", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/messages = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	var message struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	res.Decode(t, &message)
	if message.Type != "message" {
		t.Errorf("type = %q, want \"message\" — the reply must stay Claude-shaped", message.Type)
	}
	if len(message.Content) == 0 || message.Content[0].Text != "upstream reply" {
		t.Errorf("content = %+v, want the upstream text block", message.Content)
	}

	sent := upstream.Last(t)
	if got := sent.Model(t); got != "MiniMax-M3" {
		t.Errorf("upstream model = %q, want MiniMax-M3", got)
	}
	// The body must already be Claude Messages: no OpenAI conversion happened.
	var forwarded struct {
		System   any `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(sent.Body, &forwarded); err != nil {
		t.Fatalf("decode forwarded body: %v (%s)", err, truncate(sent.Body))
	}
	if len(forwarded.Messages) == 0 || forwarded.Messages[0].Role != "user" {
		t.Errorf("forwarded messages = %+v, want the client's own Claude turns", forwarded.Messages)
	}

	// The credential is the OAuth bearer beside the placeholder x-api-key the
	// gateway demands; a missing placeholder is a 401 from MiniMax.
	if got := sent.Header.Get("Authorization"); got != "Bearer mcode-access" {
		t.Errorf("Authorization = %q, want the OAuth bearer", got)
	}
	if got := sent.Header.Get("x-api-key"); got == "" {
		t.Error("x-api-key is empty; the mavis gateway requires the placeholder beside the bearer")
	}
	for header, want := range map[string]string{
		"User-Agent":       "MiniMaxAgent",
		"X-Mavis-Agent-Id": "main",
	} {
		if got := sent.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if sent.Header.Get("X-Mavis-Timezone-Offset") == "" {
		t.Error("X-Mavis-Timezone-Offset is missing; the gateway reads the client offset from it")
	}
	if sent.Header.Get("X-Mavis-Session-Id") == "" {
		t.Error("X-Mavis-Session-Id is missing; each turn of a chat must ride one session")
	}
}

// TestMiniMaxCodeCreditsRefusalFailsOver is the behaviour the whole quota lane
// exists for: a credits refusal reported as 402 with a CJK body must reach the
// client as a 429 rate_limit_error — the signal that moves combo fallback to the
// next account — instead of sitting at 402 where the account loop would treat
// it as a credential problem and spend a single-use refresh token per request.
func TestMiniMaxCodeCreditsRefusalFailsOver(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"402 credits refusal in Chinese", http.StatusPaymentRequired, `{"base_resp":{"status_code":1004,"status_msg":"积分不足，请充值"}}`},
		{"403 credits refusal in English", http.StatusForbidden, `{"error":{"message":"insufficient balance"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newEnv(t)
			spent := env.NewUpstream(t, JSONResponder(tt.status, tt.body))
			addMCodeConnection(t, env, "conn-mcode", spent)

			res := env.Post(t, "/v1/messages", claudeMessagesBody("minimax-code/MiniMax-M3", false))
			if res.Status != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429 (body: %s)", res.Status, truncate(res.Body))
			}

			var env2 struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			res.Decode(t, &env2)
			if env2.Error.Type != "rate_limit_error" {
				t.Errorf("error.type = %q, want rate_limit_error", env2.Error.Type)
			}
			if !strings.Contains(env2.Error.Message, "usage limit reached") {
				t.Errorf("error.message = %q, want the usage-limit wording", env2.Error.Message)
			}
			if got := spent.Count(); got == 0 {
				t.Error("the account was never tried")
			}
		})
	}
}

// A 403 that does NOT name the balance is a real auth refusal: it must keep its
// status so the on-401 refresh path still sees it, rather than being rewritten
// into a rate limit that skips the account entirely.
func TestMiniMaxCodeAuthRefusalKeepsItsStatus(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, JSONResponder(http.StatusForbidden, `{"error":{"message":"invalid token"}}`))
	addMCodeConnection(t, env, "conn-mcode", upstream)

	res := env.Post(t, "/v1/messages", claudeMessagesBody("minimax-code/MiniMax-M3", false))
	if res.Status == http.StatusTooManyRequests {
		t.Fatalf("an auth refusal was rewritten to 429, which skips the account instead of refreshing it (body: %s)", truncate(res.Body))
	}
	if res.Status != http.StatusForbidden && res.Status != http.StatusUnauthorized {
		t.Errorf("status = %d, want the upstream's own auth status", res.Status)
	}
}

// The two mcode sites are separate accounts; a request routed to one must never
// dial the other's upstream. Both providers share the executor, so this also
// pins that the executor is registered under each id rather than only one.
func TestMiniMaxCodeGlobalSiteIsRoutable(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, JSONResponder(http.StatusOK, claudeMessagesResponseJSON))

	priority := 1
	data := `{"apiKey":"mcode-access","baseUrl":"` + upstream.URL + `/mavis/api/v1/llm/v1/messages"}`
	if err := env.Repo.CreateProviderConnectionFull("conn-mcode-global", "minimax-code-global", "oauth", "MiniMax Code Global", &priority, data); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	res := env.Post(t, "/v1/messages", claudeMessagesBody("minimax-code-global/MiniMax-M3.1-Flash-Preview", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/messages = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := upstream.Last(t).Model(t); got != "MiniMax-M3.1-Flash-Preview" {
		t.Errorf("upstream model = %q, want the bare catalog id", got)
	}
}
