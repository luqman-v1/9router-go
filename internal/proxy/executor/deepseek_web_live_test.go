package executor

import (
	context "context"
	json "encoding/json/v2"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/providers"
)

// Live deepseek-web: one real turn against chat.deepseek.com, through the
// whole executor path a gateway request takes — the token unwrap, the
// browser-fidelity transport, the proof of work, the attestation fetch, the
// stream — so what is tested is exactly what an account is judged on.
//
// It is opt-in twice over, and skips silently without both:
//
//	9ROUTER_LIVE_TESTS=1   the suite-wide live gate (see live_guard_test.go)
//	DEEPSEEK_LIVE_TOKEN    the credential, a real account's userToken
//
// The credential never belongs in the repo; it arrives by environment. One
// turn is all a live check needs — every extra turn is extra exposure for the
// account, and the point here is to observe the fingerprint being accepted,
// not to load the account.
//
// Run it locally with:
//
//	9ROUTER_LIVE_TESTS=1 DEEPSEEK_LIVE_TOKEN='{"value":"…"}' \
//		go test ./internal/proxy/executor/ -v -run TestLiveDeepSeekWeb
func TestLiveDeepSeekWebOneTurn(t *testing.T) {
	if strings.ToLower(os.Getenv("9ROUTER_LIVE_TESTS")) != "1" {
		t.Skip("live deepseek-web test: set 9ROUTER_LIVE_TESTS=1")
	}
	token := strings.TrimSpace(os.Getenv("DEEPSEEK_LIVE_TOKEN"))
	if token == "" {
		t.Skip("live deepseek-web test: set DEEPSEEK_LIVE_TOKEN to the account's userToken")
	}

	body, err := json.Marshal(map[string]any{
		"model": "deepseek-chat",
		"messages": []map[string]any{
			{"role": "user", "content": "Reply with exactly one word: pong"},
		},
	})
	if err != nil {
		t.Fatalf("marshal live body: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := &Request{
		Ctx:      context.Background(),
		Config:   &providers.ProviderConfig{BaseURL: deepSeekWebBase + deepSeekCompletionPath},
		APIKey:   token,
		Body:     body,
		IsStream: true,
	}

	started := time.Now()
	if err := ForwardDeepSeekWeb(recorder, request); err != nil {
		t.Fatalf("the live turn was refused after %s: %v", time.Since(started).Round(time.Second), err)
	}

	answer := recorder.Body.String()
	t.Logf("the upstream answered in %s with %d bytes", time.Since(started).Round(time.Second), len(answer))
	if !strings.Contains(answer, "data: [DONE]") {
		t.Errorf("the stream never terminated; body tail: %s", tailFor(answer, 200))
	}
	// The word is matched across chunks: a stream splits it arbitrarily, and
	// the point is what the model said, not how the fragments fell.
	// The no-tools path is the one the leak was reported on: this model writes
	// its native DSML even when the request declared no tools, and the answer
	// must never carry that markup to the client.
	if strings.Contains(answer, "DSML") {
		t.Errorf("DSML markup leaked into a no-tools answer:\n%s", tailFor(answer, 400))
	}
	if !strings.Contains(deepSeekStreamedText(answer), "pong") {
		t.Errorf("the answer is missing the expected word; body tail: %s", tailFor(answer, 400))
	}
}

// deepSeekStreamedText concatenates the content deltas of an OpenAI-shaped
// event stream, so a live answer can be read as the sentence the client saw.
func deepSeekStreamedText(stream string) string {
	var text strings.Builder
	for _, line := range strings.Split(stream, "\n") {
		payload, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		for _, choice := range chunk.Choices {
			text.WriteString(choice.Delta.Content)
		}
	}
	return text.String()
}

// tailFor returns the last n bytes of s for a failure message, so a long
// event stream is quoted where its end — the part that says what went wrong —
// actually is.
func tailFor(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// Live access-token refresh: the exchange a gateway runs hourly (userToken →
// short-lived Bearer via users/current), its cache, and a re-exchange after
// the cache is dropped — which is exactly what happens when the TTL lapses or
// an auth failure evicts the entry. Two read-only calls is the whole cost to
// the account; nothing here generates.
//
// The full credential renewal is a different thing and not testable headless:
// it opens a browser and waits for a human sign-in (see
// RenewDeepSeekCredential), because the site issues a userToken only to a real
// sign-in. What CAN be checked without the human is that the refresh the
// gateway does on its own still works end to end.
func TestLiveDeepSeekWebAccessTokenRefresh(t *testing.T) {
	if strings.ToLower(os.Getenv("9ROUTER_LIVE_TESTS")) != "1" {
		t.Skip("live deepseek-web test: set 9ROUTER_LIVE_TESTS=1")
	}
	token := strings.TrimSpace(os.Getenv("DEEPSEEK_LIVE_TOKEN"))
	if token == "" {
		t.Skip("live deepseek-web test: set DEEPSEEK_LIVE_TOKEN to the account's userToken")
	}
	userToken := deepSeekUserToken(token)
	if userToken == "" {
		t.Fatal("the pasted credential unwrapped to no userToken")
	}

	ctx := context.Background()
	turn := newDeepSeekTurn(nil, deepSeekWebBase+deepSeekCompletionPath, deepSeekStealthFor(nil)).
		withBrowserFingerprint("", "")
	deepSeekTokens.drop(userToken)

	started := time.Now()
	first, err := turn.acquireAccessToken(ctx, userToken)
	if err != nil {
		t.Fatalf("the fresh exchange was refused after %s: %v", time.Since(started).Round(time.Second), err)
	}
	if len(first) < 20 {
		t.Fatalf("the exchange returned a %d-character access token, want a real one", len(first))
	}

	// The exchange is memoized: the next turn on this credential must be
	// handed the same token without a second upstream call.
	cached, ok := deepSeekTokens.get(userToken)
	if !ok || cached != first {
		t.Fatalf("the cache holds %q (present=%t), want the exchanged %q", cached, ok, first)
	}

	// A dropped entry is the expiry path: the credential has to exchange
	// again, and the site has to answer again.
	deepSeekTokens.drop(userToken)
	refreshed, err := turn.acquireAccessToken(ctx, userToken)
	if err != nil {
		t.Fatalf("the re-exchange after the cache drop was refused: %v", err)
	}
	if len(refreshed) < 20 {
		t.Fatalf("the re-exchange returned a %d-character access token, want a real one", len(refreshed))
	}
	t.Logf("the credential exchanged in %s, cached, and re-exchanged after eviction",
		time.Since(started).Round(time.Second))
}

// liveToolCall is one reconstructed tool call from an OpenAI-shaped stream.
type liveToolCall struct {
	id   string
	name string
	args string
}

// parseLiveToolStream folds an OpenAI-shaped event stream back into the
// assistant content, the tool calls (keyed by their index), and the finish
// reason. It is the client's-eye view of what the executor wrote.
func parseLiveToolStream(stream string) (content string, calls map[int]liveToolCall, finish string) {
	calls = make(map[int]liveToolCall)
	var contentBuilder strings.Builder
	for _, line := range strings.Split(stream, "\n") {
		payload, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
			contentBuilder.WriteString(choice.Delta.Content)
			for _, tc := range choice.Delta.ToolCalls {
				c := calls[tc.Index]
				if tc.ID != "" {
					c.id = tc.ID
				}
				if tc.Function.Name != "" {
					c.name = tc.Function.Name
				}
				c.args += tc.Function.Arguments
				calls[tc.Index] = c
			}
		}
	}
	return contentBuilder.String(), calls, finish
}

// liveToolTurn runs one real turn with a declared read tool through
// ForwardDeepSeekWeb and returns the stream the client saw.
func liveToolTurn(t *testing.T, token, prompt string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model": "deepseek-chat",
		"messages": []map[string]any{
			{"role": "user", "content": prompt},
		},
		"tools": []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name":        "read",
					"description": "read a URL",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path": map[string]any{"type": "string"},
							"i":    map[string]any{"type": "string"},
						},
						"required": []string{"path", "i"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal live body: %v", err)
	}
	recorder := httptest.NewRecorder()
	request := &Request{
		Ctx:      context.Background(),
		Config:   &providers.ProviderConfig{BaseURL: deepSeekWebBase + deepSeekCompletionPath},
		APIKey:   token,
		Body:     body,
		IsStream: true,
	}
	started := time.Now()
	if err := ForwardDeepSeekWeb(recorder, request); err != nil {
		t.Fatalf("the live tool turn was refused after %s: %v", time.Since(started).Round(time.Second), err)
	}
	stream := recorder.Body.String()
	t.Logf("the upstream answered in %s with %d bytes", time.Since(started).Round(time.Second), len(stream))
	return stream
}

// TestLiveDeepSeekWebToolCalls is the end-to-end tool-calling check: a real
// turn against chat.deepseek.com with a declared tool, through the whole
// executor path. The model may answer in DSML or JSON; either dialect must be
// recovered into tool_calls, and neither may leak into the assistant content.
func TestLiveDeepSeekWebToolCalls(t *testing.T) {
	if strings.ToLower(os.Getenv("9ROUTER_LIVE_TESTS")) != "1" {
		t.Skip("live deepseek-web test: set 9ROUTER_LIVE_TESTS=1")
	}
	token := strings.TrimSpace(os.Getenv("DEEPSEEK_LIVE_TOKEN"))
	if token == "" {
		t.Skip("live deepseek-web test: set DEEPSEEK_LIVE_TOKEN to the account's userToken")
	}

	stream := liveToolTurn(t, token, "You MUST call the read tool now, with path https://wttr.in/Jakarta?format=j1 and i \"Fetching Jakarta weather\". Reply with nothing else.")
	if strings.Contains(stream, "DSML") {
		t.Errorf("DSML markup leaked into the stream:\n%s", tailFor(stream, 400))
	}
	content, calls, finish := parseLiveToolStream(stream)
	if len(calls) == 0 {
		t.Fatalf("no tool_calls were recovered; finish=%q content=%q tail=%s", finish, content, tailFor(stream, 300))
	}
	for i, c := range calls {
		if c.name != "read" {
			t.Errorf("call %d name = %q, want read", i, c.name)
		}
		if !strings.Contains(c.args, "wttr.in/Jakarta") {
			t.Errorf("call %d arguments = %q, want the weather URL", i, c.args)
		}
	}
	if finish != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls", finish)
	}
	t.Logf("recovered %d tool call(s): %v", len(calls), calls)
}

// TestLiveDeepSeekWebMultiToolCalls pins the plural path end to end: one turn
// must recover two calls in one answer, each with its own arguments.
func TestLiveDeepSeekWebMultiToolCalls(t *testing.T) {
	if strings.ToLower(os.Getenv("9ROUTER_LIVE_TESTS")) != "1" {
		t.Skip("live deepseek-web test: set 9ROUTER_LIVE_TESTS=1")
	}
	token := strings.TrimSpace(os.Getenv("DEEPSEEK_LIVE_TOKEN"))
	if token == "" {
		t.Skip("live deepseek-web test: set DEEPSEEK_LIVE_TOKEN to the account's userToken")
	}

	prompt := "Call the read tool TWICE in one single reply: first with path https://wttr.in/Jakarta?format=j1 and i \"Fetching Jakarta weather\", then with path https://wttr.in/Tokyo?format=j1 and i \"Fetching Tokyo weather\". Both calls must be in the same reply."
	stream := liveToolTurn(t, token, prompt)
	if strings.Contains(stream, "DSML") {
		t.Errorf("DSML markup leaked into the stream:\n%s", tailFor(stream, 400))
	}
	content, calls, finish := parseLiveToolStream(stream)
	if len(calls) != 2 {
		t.Fatalf("recovered %d tool call(s), want 2; finish=%q content=%q tail=%s", len(calls), finish, content, tailFor(stream, 400))
	}
	seenJakarta, seenTokyo := false, false
	for i, c := range calls {
		if c.name != "read" {
			t.Errorf("call %d name = %q, want read", i, c.name)
		}
		if strings.Contains(c.args, "wttr.in/Jakarta") {
			seenJakarta = true
		}
		if strings.Contains(c.args, "wttr.in/Tokyo") {
			seenTokyo = true
		}
	}
	if !seenJakarta || !seenTokyo {
		t.Errorf("calls must cover both URLs; jakarta=%v tokyo=%v calls=%v", seenJakarta, seenTokyo, calls)
	}
	if finish != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls", finish)
	}
	t.Logf("recovered %d tool call(s): %v", len(calls), calls)
}
