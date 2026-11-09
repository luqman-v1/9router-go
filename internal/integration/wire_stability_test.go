//go:build integration

package integration

import (
	"bytes"
	"net/http"
	"testing"
)

// assertStableForwarding POSTs one logical body n times and fails unless every
// forwarded byte matches the first: prefix-caching upstreams key their cache on
// the longest byte-identical request prefix, so one reshuffle is a full miss.
func assertStableForwarding(t *testing.T, env *Env, up *Upstream, body map[string]any, n int) {
	t.Helper()

	var first []byte
	for i := range n {
		res := env.Post(t, "/v1/chat/completions", body)
		if res.Status != http.StatusOK {
			t.Fatalf("request %d/%d: gateway returned %d: %s", i+1, n, res.Status, truncate(res.Body))
		}
		got := up.Last(t).Body
		if i == 0 {
			first = append([]byte(nil), got...)
			continue
		}
		if !bytes.Equal(first, got) {
			t.Fatalf("request %d/%d forwarded a different body:\nfirst: %s\ngot:   %s",
				i+1, n, truncate(first), truncate(got))
		}
	}
	if up.Count() != n {
		t.Fatalf("upstream saw %d requests for %d POSTs — per-request comparison above is not valid", up.Count(), n)
	}
}

func TestForwardedBodyByteStableAcrossIdenticalRequests(t *testing.T) {
	env, up := newProviderEnv(t)
	body := ChatBody("deepseek/step-5-preview", false)
	body["reasoning_effort"] = "high"
	assertStableForwarding(t, env, up, body, 50)
}

func TestAnthropicLaneBodyByteStableAcrossIdenticalRequests(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, claudeMessagesResponder())
	addAnthropicConnection(t, env, "conn-anthropic", up)
	body := map[string]any{
		"model":            "claude/claude-sonnet-4-5",
		"messages":         []map[string]any{{"role": "user", "content": "hi"}},
		"reasoning_effort": "high",
	}
	assertStableForwarding(t, env, up, body, 50)
}
