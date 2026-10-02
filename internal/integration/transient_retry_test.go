//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// opencode-zen's backend answers `503 service_overloaded` in bursts while it is
// full. The gateway used to surface that to the client and lock the account,
// even though the very next attempt was served — so a transient blip cost a
// whole agent turn. Upstream retries these statuses inside one request
// (open-sse/executors/base.js DEFAULT_RETRY_CONFIG); these cases pin that the Go
// path does too, through the production router rather than the helper alone.

const zenOverloadedBody = `{"error":{"code":"service_overloaded","message":"Error from provider (Console): The backend is temporarily overloaded. Please retry.","type":"server_error"}}`

func zenResponsesStream(text string) string {
	return "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_zen\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"" + text + "\"}]}]}}\n\n"
}

func zenResponsesRequest() map[string]any {
	return map[string]any{
		"model":  "ocz/muse-spark-1.3",
		"stream": true,
		"input": []map[string]any{
			{"type": "message", "role": "user", "content": []map[string]any{
				{"type": "input_text", "text": "ping"},
			}},
		},
	}
}

// The client-facing contract: a request the upstream recovered from must reach
// the client as a served answer, not as the 503 that happened first.
func TestOpenCodeZenRecoversFromTransientOverload(t *testing.T) {
	env := newEnv(t)
	var hits atomic.Int32
	up := env.NewUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(zenOverloadedBody))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(zenResponsesStream("recovered")))
	})
	addZenConnection(t, env, up)

	res := env.Post(t, "/v1/responses", zenResponsesRequest())
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body %s — a recovered upstream must serve the turn", res.Status, truncate(res.Body))
	}
	if !strings.Contains(string(res.Body), "recovered") {
		t.Errorf("the served answer must carry upstream text, got %s", truncate(res.Body))
	}
	if got := hits.Load(); got < 2 {
		t.Errorf("expected the gateway to re-send after the 503, got %d attempt(s)", got)
	}
}

// A 503 is the upstream's own capacity signal, not a client fault: replaying it
// must not be mistaken for a broken request and answered with a 400.
func TestOpenCodeZenRetryKeepsTheSameModelOnTheWire(t *testing.T) {
	env := newEnv(t)
	var hits atomic.Int32
	up := env.NewUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(zenOverloadedBody))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(zenResponsesStream("same-model")))
	})
	addZenConnection(t, env, up)

	res := env.Post(t, "/v1/responses", zenResponsesRequest())
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.Status, truncate(res.Body))
	}

	sent := up.Last(t)
	if sent.Path != "/zen/v1/responses" {
		t.Fatalf("upstream path = %q, want /zen/v1/responses", sent.Path)
	}
	if got := sent.Model(t); got != "muse-spark-1.3" {
		t.Errorf("the retry must re-send the same bare model id, got %q", got)
	}
}

// A 400 is our bug or the client's: replaying it only multiplies the damage and
// hides the real error. The client must see it on the first answer.
func TestOpenCodeZenDoesNotRetryClientErrors(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, JSONResponder(http.StatusBadRequest, `{"error":{"message":"unknown model"}}`))
	addZenConnection(t, env, up)

	res := env.Post(t, "/v1/responses", zenResponsesRequest())
	if res.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want the upstream 400 verbatim, body %s", res.Status, truncate(res.Body))
	}
	if got := up.Count(); got != 1 {
		t.Errorf("a 400 must not be re-sent, upstream saw %d requests", got)
	}
}

// A 429 names an account-level quota window. Repeating it here would deepen the
// throttle on the same credential, so it must reach account fallback untouched.
func TestOpenCodeZenDoesNotRetryRateLimits(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, JSONResponder(http.StatusTooManyRequests, `{"error":{"message":"rate limit exceeded","type":"rate_limit_error"}}`))
	addZenConnection(t, env, up)

	res := env.Post(t, "/v1/responses", zenResponsesRequest())
	if res.Status == http.StatusOK {
		t.Fatal("a rate-limited upstream must not produce a served answer")
	}
	if got := up.Count(); got != 1 {
		t.Errorf("a 429 must be handed to account fallback unrepeated, upstream saw %d requests", got)
	}
}

// The retry must not outlast the client: a turn nobody reads must stop instead
// of holding a connection open through the full backoff.
func TestOpenCodeZenRetryStopsWhenTheClientDisconnects(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(zenOverloadedBody))
	})
	addZenConnection(t, env, up)

	// Sent directly rather than through Env.Do so the exchange can carry its own
	// deadline — a cancelled client must end the request mid-backoff.
	payload, err := json.Marshal(zenResponsesRequest())
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, env.BaseURL+"/v1/responses", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+env.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatal("an always-overloaded upstream cannot serve this turn")
		}
	}
	elapsed := time.Since(start)

	// The 503 policy waits 2s per retry; a client that left in 300ms must not
	// be kept for a backoff it cannot read.
	if elapsed > 3*time.Second {
		t.Errorf("client hung up after 300ms but the request ran %v", elapsed)
	}
}
