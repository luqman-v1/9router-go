//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// piiEmail is the value a pii policy must catch. It is a plain address in a
// plain sentence, so a detector that stops matching it is a regression rather
// than a fixture problem.
const piiEmail = "billing@acme-corp.com"

// addPIIPolicy stores a global pii policy at the given action, which is how an
// operator configures guardrails: one scope, one detector set, one action.
func (e *Env) addPIIPolicy(t *testing.T, action string) {
	t.Helper()
	p := &db.GuardrailPolicy{
		Scope:   string("global"),
		Name:    "integration pii",
		Enabled: true,
		Config:  `{"detectors":["pii"],"action":"` + action + `"}`,
	}
	if err := e.Repo.CreateGuardrailPolicy(p); err != nil {
		t.Fatalf("create guardrail policy: %v", err)
	}
}

// completionWithEmail is a well-formed chat completion whose answer carries an
// address, so the outbound tap has something real to judge.
func completionWithEmail() string {
	return `{"id":"chatcmpl-integration","object":"chat.completion","created":1700000000,` +
		`"model":"upstream-model","choices":[{"index":0,"message":{"role":"assistant",` +
		`"content":"Send the invoice to ` + piiEmail + ` please."},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`
}

// TestGuardrailMasksNonStreamedResponse is the contract a client depends on: the
// address the provider produced never reaches it. Everything else about the
// response is unchanged, so a policy cannot break an integration that was
// working before it was configured.
func TestGuardrailMasksNonStreamedResponse(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, JSONResponder(http.StatusOK, completionWithEmail()))
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Integration", up, "sk-upstream")
	env.addPIIPolicy(t, "mask")

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false))

	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Status, truncate(res.Body))
	}
	if strings.Contains(string(res.Body), piiEmail) {
		t.Fatalf("address reached the client unmasked: %s", truncate(res.Body))
	}
	if !strings.Contains(string(res.Body), "[REDACTED]") {
		t.Errorf("response was not masked: %s", truncate(res.Body))
	}
}

// TestGuardrailMasksStreamedResponse is the half a per-frame scanner gets
// wrong. The gateway relays SSE a chunk at a time, and the address arrives
// split across two frames, so a tap that judged each frame alone would send
// both halves to the client.
func TestGuardrailMasksStreamedResponse(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// The address is split mid-value, exactly as a token stream does it.
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"Send it to billing@acme-"},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"corp.com now."},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Integration", up, "sk-upstream")
	env.addPIIPolicy(t, "mask")

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", true))

	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Status, truncate(res.Body))
	}
	body := string(res.Body)
	if strings.Contains(body, "billing@acme-corp.com") {
		t.Fatalf("split address reached the client unmasked: %s", truncate(res.Body))
	}
	if strings.Contains(body, "billing@acme-") || strings.Contains(body, "corp.com now") {
		t.Errorf("a half of the address survived: %s", truncate(res.Body))
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Errorf("stream was not masked: %s", truncate(res.Body))
	}
	// A stream that ends without its terminator reads as hung to a strict
	// client, so masking must not break the framing it is sitting in.
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("masked stream lost its terminator: %s", truncate(res.Body))
	}
}

// TestGuardrailBlockStopsTurnWithoutFailover is the property that makes block
// safe to configure. The content was refused once; failing over would send the
// identical prompt to the next model and spread it across every provider the
// combo names.
func TestGuardrailBlockStopsTurnWithoutFailover(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, JSONResponder(http.StatusOK, completionWithEmail()))
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Integration", up, "sk-upstream")
	// A second connection of a different provider stands in for the account a
	// fallback would have moved to.
	second := env.NewUpstream(t, JSONResponder(http.StatusOK, completionWithEmail()))
	env.AddConnection(t, "conn-qwen", "qwen", "Qwen Integration", second, "sk-upstream")
	env.AddCombo(t, "combo-1", "guarded combo", []string{"deepseek/deepseek-chat", "qwen/qwen-max"})
	env.addPIIPolicy(t, "block")

	res := env.Post(t, "/v1/chat/completions", ChatBody("guarded combo", false))

	if second.Count() != 0 {
		t.Errorf("refused content was replayed to another provider (%d requests)", second.Count())
	}
	if strings.Contains(string(res.Body), piiEmail) {
		t.Fatalf("address reached the client despite block: %s", truncate(res.Body))
	}
	if res.Status == http.StatusOK {
		t.Errorf("blocked turn reported success: %s", truncate(res.Body))
	}
	if msg := res.ErrorMessage(t); !strings.Contains(msg, "guardrail") {
		t.Errorf("client was not told why: %q", msg)
	}
}

// TestGuardrailBlockTerminatesStream proves a blocked stream ends rather than
// hanging. The status line is spent before the first token, so the refusal can
// only be reported in-band — and a stream with no terminal frame is read by
// strict clients as a request that never came back.
func TestGuardrailBlockTerminatesStream(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"Send it to ` + piiEmail + `"},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Integration", up, "sk-upstream")
	env.addPIIPolicy(t, "block")

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", true))

	body := string(res.Body)
	if strings.Contains(body, piiEmail) {
		t.Fatalf("blocked address reached the client: %s", truncate(res.Body))
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("blocked stream was left unterminated: %s", truncate(res.Body))
	}
}

// TestGuardrailWritesAuditRow proves the decision is recorded. An operator who
// configures a policy with no way to see it fire has no way to tell a blocked
// turn from a broken provider.
func TestGuardrailWritesAuditRow(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, JSONResponder(http.StatusOK, completionWithEmail()))
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Integration", up, "sk-upstream")
	env.addPIIPolicy(t, "mask")

	env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false))

	logs, err := env.Repo.ListGuardrailLogs(50)
	if err != nil {
		t.Fatalf("read guardrail audit log: %v", err)
	}
	var outbound int
	for _, l := range logs {
		if l.Direction == "outbound" {
			outbound++
			if l.Detector != "email" {
				t.Errorf("detector = %q, want email", l.Detector)
			}
			if l.Action != "mask" {
				t.Errorf("action = %q, want mask", l.Action)
			}
		}
	}
	if outbound == 0 {
		t.Fatal("no outbound decision was recorded in guardrail_logs")
	}
}

// TestGuardrailDisabledLeavesTrafficUntouched is the backward-compatibility
// guarantee: with no policy the response path must be byte-identical, or
// configuring guardrails would change traffic that has nothing to do with it.
func TestGuardrailDisabledLeavesTrafficUntouched(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, JSONResponder(http.StatusOK, completionWithEmail()))
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Integration", up, "sk-upstream")

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false))

	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Status, truncate(res.Body))
	}
	if !strings.Contains(string(res.Body), piiEmail) {
		t.Errorf("response was filtered with no policy configured: %s", truncate(res.Body))
	}
	logs, err := env.Repo.ListGuardrailLogs(50)
	if err != nil {
		t.Fatalf("read guardrail audit log: %v", err)
	}
	if len(logs) != 0 {
		t.Errorf("recorded %d decisions with no policy configured", len(logs))
	}
}
