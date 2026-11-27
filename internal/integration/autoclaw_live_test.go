//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// TestAutoclawEndToEnd drives the real router and database through an
// AutoClaw connection whose fake upstream mirrors the desktop protocol:
// signed headers, refresh-token rotation on every refresh, and an SSE-only
// chat response. No real credential or network is needed.
func TestAutoclawEndToEnd(t *testing.T) {
	env := newEnv(t)
	var refreshCount atomic.Int32
	var rotatedRT atomic.Value
	rotatedRT.Store("rt-live-0")

	up := env.NewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/userapi/v1/"):
			n := refreshCount.Add(1)
			newRT := fmt.Sprintf("rt-live-%d", n)
			rotatedRT.Store(newRT)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"code":0,"data":{"access_token":"at-live","refresh_token":%q}}`, newRT)))
		default:
			// The signature must be the operator's app key, and the model
			// must ride in the header even though the chat dispatcher
			// leaves Request.ModelName unset.
			if got := r.Header.Get("X-Request-Model"); got != "zai_auto" {
				t.Errorf("X-Request-Model = %q, want zai_auto (from body)", got)
			}
			if got := r.Header.Get("X-Auth-Appid"); got != "100003" {
				t.Errorf("X-Auth-Appid = %q", got)
			}
			if got := r.Header.Get("X-Version"); got != "2.6.1" {
				t.Errorf("X-Version = %q, want 2.6.1", got)
			}
			frames := []string{
				`{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"live"},"finish_reason":null}]}`,
				`{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" ok"},"finish_reason":"stop"}]}`,
			}
			for _, f := range frames {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", f)
			}
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}
	})

	// AutoClaw computes paths itself; store the bare upstream base.
	data := fmt.Sprintf(`{"apiKey":%q,"baseUrl":%q}`, "rt-live-0|did-live|app-key-live", up.URL)
	if err := env.Repo.CreateProviderConnectionFull(t.Context(), "conn-autoclaw", "autoclaw", "apikey", "Autoclaw Live", nil, data); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	res := env.Do(t, http.MethodPost, "/v1/chat/completions", map[string]any{
		"model":    "autoclaw/zai_auto",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
		"stream":   false,
	}, WithAPIKey(env.APIKey))

	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Status, string(res.Body))
	}
	var got struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(res.Body, &got); err != nil {
		t.Fatalf("json: %v (%s)", err, string(res.Body))
	}
	if len(got.Choices) == 0 || got.Choices[0].Message.Content != "live ok" {
		t.Errorf("content = %+v, want 'live ok'", got.Choices)
	}
	if refreshCount.Load() < 1 {
		t.Errorf("refresh was never called")
	}
	if got := rotatedRT.Load().(string); got == "rt-live-0" {
		t.Errorf("upstream rotated the refresh token but the executor reused the old one")
	}
	for _, req := range up.Requests() {
		if !strings.HasPrefix(req.Path, "/userapi/v1/") {
			continue
		}
		var in map[string]string
		if err := json.Unmarshal(req.Body, &in); err != nil {
			t.Fatalf("refresh body: %v", err)
		}
		if in["source_id"] != "autoclaw" || in["device_id"] != "did-live" {
			t.Errorf("refresh body = %v, want source_id=autoclaw device_id=did-live", in)
		}
	}
}
