//go:build integration

package integration

import (
	"os"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// nvidiaLiveKey returns the live key or skips the test. Every test in this file
// is a live test: it talks to integrate.api.nvidia.com and is skipped in CI,
// where no upstream credential exists.
func nvidiaLiveKey(t *testing.T) string {
	t.Helper()
	key := strings.TrimSpace(os.Getenv("NVIDIA_LIVE_KEY"))
	if key == "" {
		t.Skip("NVIDIA_LIVE_KEY not set; skipping live NVIDIA test")
	}
	return key
}

// nvidiaLiveModel is the one model on this key that is not end-of-life. The
// registry's other entries answered 410 "reached its end of life" when probed
// on 2026-10-29, so a test pinned to them would report an upstream retirement
// as a gateway regression.
const nvidiaLiveModel = "nvidia/nemotron-3-ultra-550b-a55b"

// nvidiaGateway boots an Env whose only provider is a custom OpenAI-compatible
// node pointed at integrate.api.nvidia.com through a recording proxy.
//
// NVIDIA accepts both max_tokens and max_completion_tokens with a 200 — probed
// on 2026-10-29, streaming and non-streaming alike — so the HTTP status cannot
// distinguish a gateway that strips the duplicate from one that forwards both.
// The recorded bytes can, and that is what these tests assert.
func nvidiaGateway(t *testing.T) (*Env, *recordingProxy) {
	t.Helper()
	key := nvidiaLiveKey(t)

	env := newEnv(t)
	proxy := newRecordingProxy(t, "https://integrate.api.nvidia.com/v1/chat/completions", key)

	const nodeID = "openai-compatible-chat-nvidia-live"
	if _, err := env.Repo.CreateProviderNode(nodeID, "openai-compatible", "NVIDIA Live",
		`{"prefix":"nv","apiType":"chat"}`); err != nil {
		t.Fatalf("create NVIDIA provider node: %v", err)
	}
	priority := 1
	connData := `{"apiKey":"sk-through-proxy","baseUrl":"` + proxy.URL + `","providerSpecificData":{"apiType":"chat"}}`
	if err := env.Repo.CreateProviderConnectionFull("conn-nvidia-live", nodeID, "apikey", "NVIDIA Live", &priority, connData); err != nil {
		t.Fatalf("create NVIDIA connection: %v", err)
	}
	// The gateway's parity default User-Agent is undici's "node"; the live
	// endpoint is probed with curl's so a bot filter can never be mistaken for
	// the token-field behaviour under test.
	if err := env.Repo.SetProviderOverride(nodeID, &db.ProviderOverrides{
		Headers: map[string]string{"User-Agent": "curl/8.5.0"},
	}); err != nil {
		t.Fatalf("set NVIDIA User-Agent override: %v", err)
	}
	return env, proxy
}

// TestLiveNvidiaDoubleTokenField is the end-to-end proof against a real
// upstream: the client sends both spellings, and the body the gateway puts on
// the wire carries exactly one.
func TestLiveNvidiaDoubleTokenField(t *testing.T) {
	env, proxy := nvidiaGateway(t)

	res := env.Post(t, "/v1/chat/completions", map[string]any{
		"model": "nv/" + nvidiaLiveModel, "stream": true,
		"max_tokens": 32, "max_completion_tokens": 64,
		"messages": []map[string]any{{"role": "user", "content": "say ok"}},
	})

	assertSingleTokenField(t, proxy, true)
	requireReachedNvidia(t, res)
}

// TestLiveNvidiaComboDoubleTokenField drives the same request through a combo,
// which is the lane the original report came from: a combo member attempt goes
// through tryForwardWithConnection, the same guard site as a single attempt.
func TestLiveNvidiaComboDoubleTokenField(t *testing.T) {
	env, proxy := nvidiaGateway(t)

	env.AddCombo(t, "live-combo", "livecombo", []string{"nv/" + nvidiaLiveModel})

	res := env.Post(t, "/v1/chat/completions", map[string]any{
		"model": "livecombo", "stream": true,
		"max_tokens": 32, "max_completion_tokens": 64,
		"messages": []map[string]any{{"role": "user", "content": "say ok"}},
	})

	assertSingleTokenField(t, proxy, true)
	requireReachedNvidia(t, res)
}

// TestLiveNvidiaSingleTokenFieldUnchanged pins that the guard is a no-op when
// the client sent one spelling: the value must survive, not be replaced by the
// other one and not be dropped.
func TestLiveNvidiaSingleTokenFieldUnchanged(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		want bool // whether max_completion_tokens is the surviving field
	}{
		{
			name: "max_tokens only",
			body: map[string]any{"max_tokens": 32},
			want: false,
		},
		{
			name: "max_completion_tokens only",
			body: map[string]any{"max_completion_tokens": 64},
			want: true,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			env, proxy := nvidiaGateway(t)

			payload := map[string]any{
				"model": "nv/" + nvidiaLiveModel, "stream": true,
				"messages": []map[string]any{{"role": "user", "content": "say ok"}},
			}
			for k, v := range tt.body {
				payload[k] = v
			}

			res := env.Post(t, "/v1/chat/completions", payload)

			assertSingleTokenField(t, proxy, tt.want)
			requireReachedNvidia(t, res)
		})
	}
}

// requireReachedNvidia fails on the regression these tests exist for, and skips
// when the live endpoint is the thing that is unwell.
//
// The token-field assertion is deterministic and always enforced: it reads the
// bytes the gateway put on the wire, which no upstream weather can change. The
// HTTP status is not deterministic, so it is only used to tell three cases
// apart:
//
//   - a 200 means the request reached a model;
//   - an upstream-origin failure means the endpoint is unwell (a retired model
//     answers 410, an account without access 404 "Not found for account", and a
//     busy backend 503 "Service temporarily overloaded") — none of which says
//     anything about the gateway, so it skips;
//   - anything else is a gateway defect and fails.
//
// The `cannot both be set` message is the one upstream answer that IS this bug,
// so it fails hard and can never be mistaken for weather.
func requireReachedNvidia(t *testing.T, res *Result) {
	t.Helper()
	if res.Status == 200 {
		return
	}
	body := string(res.Body)
	if strings.Contains(body, "cannot both be set") {
		t.Fatalf("upstream rejected both token fields: %s", truncate(res.Body))
	}
	// The gateway tags a failure it received from upstream with either
	// origin=upstream or type=upstream_error; both mean the request left here.
	if strings.Contains(body, `"origin":"upstream"`) || strings.Contains(body, `"type":"upstream_error"`) {
		t.Skipf("live upstream unavailable (status %d): %s", res.Status, truncate(res.Body))
	}
	t.Fatalf("gateway status = %d, want 200; body: %s", res.Status, truncate(res.Body))
}
