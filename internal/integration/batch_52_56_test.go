//go:build integration

package integration

import (
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// This file pins the client-visible half of #52, #53, #55 and #56 through the
// real router, the real middleware stack and a real HTTP listener. Unit tests
// already cover the helpers; what they cannot see is whether the sanitized
// schema, the failover and the reset fields actually reach the wire.

// geminiSSE is the streamGenerateContent shape the Gemini executor expects.
const geminiSSE = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]}," +
	"\"finishReason\":\"STOP\",\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":10," +
	"\"candidatesTokenCount\":2,\"totalTokenCount\":12}}\n\n"

func geminiOK(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(geminiSSE))
	}
}

// lastRequestBody returns the raw JSON body the gateway last sent upstream.
// This is the only place the sanitized payload can be observed from outside
// the translator package.
func lastRequestBody(t *testing.T, up *Upstream) string {
	t.Helper()
	return string(up.Last(t).Body)
}

// addAntigravityConnection stores an OAuth-shaped antigravity connection
// pointed at up. AddConnection cannot be reused here: it hardcodes the
// apikey auth type and an OpenAI-style baseUrl, and antigravity is forwarded
// through TranslateOpenAIToGemini against CloudCode's v1internal endpoint.
func addAntigravityConnection(t *testing.T, env *Env, up *Upstream) {
	t.Helper()
	priority := 1
	data := fmt.Sprintf(`{"accessToken":"ya29.e2e","refreshToken":"r-e2e",`+
		`"expiresAt":%q,"projectId":"e2e-project","baseUrl":%q}`,
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339), up.URL)
	if err := env.Repo.CreateProviderConnectionFull("conn-ag", "antigravity", "oauth",
		"Antigravity E2E", &priority, data); err != nil {
		t.Fatalf("create antigravity connection: %v", err)
	}
	stored, err := env.Repo.GetProviderConnectionByID("conn-ag")
	if err != nil || stored == nil {
		t.Fatalf("antigravity connection not stored: %v", err)
	}
	if !strings.Contains(stored.Data, up.URL) {
		t.Fatalf("connection data %q does not carry the fake upstream %q", stored.Data, up.URL)
	}
}

// TestGeminiToolSchemaDropsTupleKeywords pins #52 end to end: a draft-07 schema
// carrying additionalItems reached Google's generateContent endpoint verbatim
// before, which answers 400 "Unknown name additionalItems at
// functionDeclaration.parameters" and kills the whole tool call.
func TestGeminiToolSchemaDropsTupleKeywords(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, geminiOK(t))
	addAntigravityConnection(t, env, up)

	body := map[string]any{
		"model":    "antigravity/gemini-3.8-flash",
		"messages": []any{map[string]any{"role": "user", "content": "write rows"}},
		"tools": []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "write_rows",
				"description": "append rows",
				"parameters": map[string]any{
					"type":                 "object",
					"additionalItems":      map[string]any{"type": "string"},
					"prefixItems":          []any{map[string]any{"type": "object"}},
					"additionalProperties": false,
					"properties": map[string]any{
						"rows": map[string]any{
							"type":            "array",
							"items":           map[string]any{"type": "string"},
							"additionalItems": map[string]any{"type": "string"},
						},
					},
				},
			},
		}},
	}

	res := env.Post(t, "/v1/chat/completions", body)
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	sent := lastRequestBody(t, up)
	for _, banned := range []string{"additionalItems", "prefixItems"} {
		if strings.Contains(sent, banned) {
			t.Errorf("upstream received %q; Gemini rejects it with 400\nbody: %s", banned, truncate([]byte(sent)))
		}
	}
	// The strip must not take the schema with it.
	if !strings.Contains(sent, "write_rows") {
		t.Errorf("tool name lost during sanitizing\nbody: %s", truncate([]byte(sent)))
	}
	if !strings.Contains(sent, `"rows"`) {
		t.Errorf("tool property lost during sanitizing\nbody: %s", truncate([]byte(sent)))
	}
}

// TestGeminiKeepsToolResultImage pins #55 end to end: a browser tool that
// answers with a screenshot used to reach Gemini as nothing at all, so the
// model on the next turn could not see what the tool had captured.
func TestGeminiKeepsToolResultImage(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, geminiOK(t))
	addAntigravityConnection(t, env, up)

	body := map[string]any{
		"model": "antigravity/gemini-3.8-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": "screenshot the page"},
			map[string]any{"role": "assistant", "content": "taking it", "tool_calls": []any{
				map[string]any{"id": "call_1", "type": "function",
					"function": map[string]any{"name": "browser_screenshot", "arguments": "{}"}},
			}},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": []any{
				map[string]any{"type": "text", "text": "captured"},
				map[string]any{"type": "image_url", "image_url": map[string]any{
					"url": "data:image/png;base64,QUJD",
				}},
			}},
		},
	}

	res := env.Post(t, "/v1/chat/completions", body)
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	sent := lastRequestBody(t, up)
	if !strings.Contains(sent, "inlineData") {
		t.Errorf("tool-result image never reached Gemini as inlineData\nbody: %s", truncate([]byte(sent)))
	}
	if !strings.Contains(sent, "image/png") {
		t.Errorf("inlineData lost its mimeType\nbody: %s", truncate([]byte(sent)))
	}
	if !strings.Contains(sent, "QUJD") {
		t.Errorf("inlineData lost its base64 payload\nbody: %s", truncate([]byte(sent)))
	}
}

// TestComboExhaustedPublishesResetTiming pins #56 end to end. The client has no
// way to learn when a combo becomes usable again unless the instant travels as
// data; a header alone is invisible to a browser SDK or a log-only integration.
func TestComboExhaustedPublishesResetTiming(t *testing.T) {
	env := newEnv(t)
	reset := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339)
	up := env.NewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"quota exceeded","type":"rate_limit_error","code":429,"reset_at":"` + reset + `"}}`))
	})
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek E2E", up, "sk-upstream")
	env.AddCombo(t, "combo-quota", "Quota Combo", []string{"deepseek/deepseek-chat"})

	res := env.Post(t, "/v1/chat/completions", ChatBody("Quota Combo", false))
	if res.Status == http.StatusOK {
		t.Fatalf("exhausted combo answered 200, want the quota failure to reach the client")
	}

	if got := res.Header.Get("Retry-After"); got == "" {
		t.Errorf("Retry-After header missing on an exhausted combo; body: %s", truncate(res.Body))
	}

	var envelope struct {
		Error struct {
			ResetAt    string `json:"reset_at"`
			RetryAfter *int   `json:"retry_after"`
		} `json:"error"`
	}
	if err := json.Unmarshal(res.Body, &envelope); err != nil {
		t.Fatalf("exhausted combo body is not an error envelope: %v; body: %s", err, truncate(res.Body))
	}
	if envelope.Error.ResetAt == "" {
		t.Errorf("error.reset_at missing: %s", truncate(res.Body))
	}
	if envelope.Error.RetryAfter == nil {
		t.Errorf("error.retry_after missing: %s", truncate(res.Body))
	}
	if envelope.Error.ResetAt != "" && envelope.Error.RetryAfter != nil {
		parsed, err := time.Parse(time.RFC3339, envelope.Error.ResetAt)
		if err != nil {
			t.Errorf("error.reset_at = %q, want RFC3339: %v", envelope.Error.ResetAt, err)
		} else if parsed.Before(time.Now().Add(-time.Minute)) {
			t.Errorf("error.reset_at = %q, want a future instant", envelope.Error.ResetAt)
		}
	}
}
