//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"9router/proxy/internal/db"
)

// sakooLiveKey returns the live key or skips the test. Every test in this file
// is a live test: it talks to api.sakoo.biz.id and is skipped in CI, where no
// upstream credential exists.
func sakooLiveKey(t *testing.T) string {
	t.Helper()
	key := strings.TrimSpace(os.Getenv("SAKOO_LIVE_KEY"))
	if key == "" {
		t.Skip("SAKOO_LIVE_KEY not set; skipping live Sakoo test")
	}
	return key
}

// recordingProxy is a fake provider that records what the gateway sent and
// then relays the request to the real upstream, streaming the answer back.
//
// Recording the outbound body is the point: api.sakoo.biz.id has accepted both
// max_tokens and max_completion_tokens since the reported failure (the request
// used to be routed to GitHub models, which rejects the pair), so a 200 from
// the live endpoint alone no longer distinguishes a gateway that strips the
// duplicate field from one that forwards both. The bytes on the wire do.
type recordingProxy struct {
	*httptest.Server

	mu       sync.Mutex
	lastBody []byte
}

func newRecordingProxy(t *testing.T, upstream string, key string) *recordingProxy {
	t.Helper()
	p := &recordingProxy{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read request body: "+err.Error(), http.StatusInternalServerError)
			return
		}
		p.mu.Lock()
		p.lastBody = body
		p.mu.Unlock()

		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstream, strings.NewReader(string(body)))
		if err != nil {
			http.Error(w, "build upstream request: "+err.Error(), http.StatusBadGateway)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("User-Agent", "curl/8.5.0")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, "forward upstream: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		buf := make([]byte, 4096)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			if rerr != nil {
				return
			}
		}
	}))
	t.Cleanup(p.Server.Close)
	return p
}

// tokenFields reports which token-limit spellings the recorded body carries.
func (p *recordingProxy) tokenFields(t *testing.T) (maxTokens, maxCompletion bool) {
	t.Helper()
	p.mu.Lock()
	body := p.lastBody
	p.mu.Unlock()
	if body == nil {
		t.Fatal("gateway made no request to the recording proxy")
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode recorded body %q: %v", truncate(body), err)
	}
	_, maxTokens = payload["max_tokens"]
	_, maxCompletion = payload["max_completion_tokens"]
	return maxTokens, maxCompletion
}

// sakooGateway boots an Env whose only provider is a custom OpenAI-compatible
// node pointed at api.sakoo.biz.id through a recording proxy.
func sakooGateway(t *testing.T) (*Env, *recordingProxy) {
	t.Helper()
	key := sakooLiveKey(t)

	env := newEnv(t)
	proxy := newRecordingProxy(t, "https://api.sakoo.biz.id/v1/chat/completions", key)

	const nodeID = "openai-compatible-chat-sakoo"
	if _, err := env.Repo.CreateProviderNode(nodeID, "openai-compatible", "Sakoo",
		`{"prefix":"sak","apiType":"chat"}`); err != nil {
		t.Fatalf("create Sakoo provider node: %v", err)
	}
	priority := 1
	connData := `{"apiKey":"sk-through-proxy","baseUrl":"` + proxy.URL + `","providerSpecificData":{"apiType":"chat"}}`
	if err := env.Repo.CreateProviderConnectionFull("conn-sakoo", nodeID, "apikey", "Sakoo", &priority, connData); err != nil {
		t.Fatalf("create Sakoo connection: %v", err)
	}
	// Sakoo answers 403 client_router_denied to the gateway's parity default
	// `User-Agent: node` (undici's value). That block is independent of the
	// token fields — it reproduces with max_tokens alone — so it is neutralised
	// to keep these tests measuring the one thing they claim to measure.
	if err := env.Repo.SetProviderOverride(nodeID, &db.ProviderOverrides{
		Headers: map[string]string{"User-Agent": "curl/8.5.0"},
	}); err != nil {
		t.Fatalf("set Sakoo User-Agent override: %v", err)
	}
	return env, proxy
}

// assertSingleTokenField fails when the gateway forwarded both spellings, or
// when it forwarded neither (which would mean it dropped a limit the client set).
func assertSingleTokenField(t *testing.T, p *recordingProxy, wantCompletion bool) {
	t.Helper()
	maxTokens, maxCompletion := p.tokenFields(t)
	if maxTokens && maxCompletion {
		t.Fatalf("gateway sent both max_tokens and max_completion_tokens: %s", truncate(p.lastBody))
	}
	if !maxTokens && !maxCompletion {
		t.Fatalf("gateway sent neither token field: %s", truncate(p.lastBody))
	}
	if got := maxCompletion; got != wantCompletion {
		t.Fatalf("kept max_completion_tokens = %v, want %v; body: %s",
			got, wantCompletion, truncate(p.lastBody))
	}
}

// requireReachedModel fails on the regression this file exists for, and skips
// when the live endpoint is the thing that is unwell.
//
// The token-field assertion above is deterministic and always enforced. The
// HTTP status is not: api.sakoo.biz.id routes bansos/gpt-6-luna through GitHub
// models, which has answered 503 "shutting down", 403 client_router_denied, and
// 400 "no model endpoints available given user constraints" for the same body
// within minutes — including for single-field control requests that this fix
// does not touch. A test that hard-failed on those would report an upstream
// outage as a gateway regression, which is how a suite stops being believed.
func requireReachedModel(t *testing.T, res *Result) {
	t.Helper()
	if res.Status == http.StatusOK {
		return
	}
	// The one upstream answer that IS this bug: the pair reached a backend that
	// rejects it. Fail hard so a regression can never be mistaken for weather.
	if strings.Contains(string(res.Body), "cannot both be set") {
		t.Fatalf("upstream rejected both token fields: %s", truncate(res.Body))
	}
	if strings.Contains(string(res.Body), `"origin":"upstream"`) {
		t.Skipf("live upstream unavailable (status %d): %s", res.Status, truncate(res.Body))
	}
	t.Fatalf("gateway status = %d, want 200; body: %s", res.Status, truncate(res.Body))
}

// TestLiveSakooDoubleTokenField is the end-to-end proof of the fix: the real
// router talks to the real upstream, and the body it puts on the wire carries
// exactly one token-limit field even though the client sent two.
//
// Live repro (2026-10-28): a streaming request to api.sakoo.biz.id for
// bansos/gpt-6-luna carrying both fields was answered 400 "max_tokens and
// max_completion_tokens cannot both be set" — the request was routed to GitHub
// models, which rejects the pair on a streamed call.
func TestLiveSakooDoubleTokenField(t *testing.T) {
	env, proxy := sakooGateway(t)

	res := env.Post(t, "/v1/chat/completions", map[string]any{
		"model": "sak/bansos/gpt-6-luna", "stream": true,
		"max_tokens": 1024, "max_completion_tokens": 2048,
		"messages": []map[string]any{{"role": "user", "content": "Reply with the single word ok."}},
	})

	assertSingleTokenField(t, proxy, true)
	requireReachedModel(t, res)
	if !strings.Contains(string(res.Body), "data: ") {
		t.Fatalf("expected an SSE stream, got %q", truncate(res.Body))
	}
}

// TestLiveSakooComboDoubleTokenField drives the same request through a combo,
// which is the shape the reported failure took: the client asked for
// `combo-wombo` and the combo resolved to bansos/gpt-6-luna. A combo attempt
// takes a different dispatch path from a single-model request, so the guard has
// to hold there too.
func TestLiveSakooComboDoubleTokenField(t *testing.T) {
	env, proxy := sakooGateway(t)
	env.AddCombo(t, "combo-live-sakoo", "combo-live-sakoo", []string{"sak/bansos/gpt-6-luna"})

	res := env.Post(t, "/v1/chat/completions", map[string]any{
		"model": "combo-live-sakoo", "stream": true,
		"max_tokens": 1024, "max_completion_tokens": 2048,
		"messages": []map[string]any{{"role": "user", "content": "Reply with the single word ok."}},
	})

	assertSingleTokenField(t, proxy, true)
	requireReachedModel(t, res)
}

// TestLiveSakooSingleTokenFieldUnchanged is the control: a client that sends
// one spelling must reach the upstream with that same spelling and its value
// intact. Without it, "the gateway stopped sending both" would be
// indistinguishable from "the gateway rewrites the field unconditionally".
func TestLiveSakooSingleTokenFieldUnchanged(t *testing.T) {
	cases := []struct {
		name           string
		field          string
		wantCompletion bool
	}{
		{name: "max_tokens only", field: "max_tokens", wantCompletion: false},
		{name: "max_completion_tokens only", field: "max_completion_tokens", wantCompletion: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, proxy := sakooGateway(t)

			res := env.Post(t, "/v1/chat/completions", map[string]any{
				"model": "sak/bansos/gpt-6-luna", "stream": true,
				tc.field:   1024,
				"messages": []map[string]any{{"role": "user", "content": "Reply with the single word ok."}},
			})

			assertSingleTokenField(t, proxy, tc.wantCompletion)
			requireReachedModel(t, res)
		})
	}
}
