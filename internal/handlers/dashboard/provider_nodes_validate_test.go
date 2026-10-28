package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
)

// errConnRefused returns a *url.Error wrapping ECONNREFUSED, the error an
// http.Client produces when a probe target is unreachable.
func errConnRefused() error {
	return &urlErr{op: "dial", err: syscall.ECONNREFUSED}
}

// urlErr mimics net/url.Error enough for validateNodeNetworkMessage to classify
// it via errors.As / errors.Is against the wrapped syscall errno.
type urlErr struct {
	op  string
	err error
}

func (e *urlErr) Error() string   { return e.op + ": " + e.err.Error() }
func (e *urlErr) Unwrap() error   { return e.err }
func (e *urlErr) Timeout() bool   { return false }
func (e *urlErr) Temporary() bool { return false }

// postValidateNode posts a provider-node validation request and returns the
// recorder plus decoded JSON body. local=true sets a loopback RemoteAddr so the
// SSRF gate is skipped (mirrors a browser fetch from the local dashboard).
func postValidateNode(t *testing.T, body string, local bool) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	req := httptest.NewRequest(http.MethodPost, "/api/provider-nodes/validate", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	if local {
		req.RemoteAddr = "127.0.0.1:3456"
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var out map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

func nodeOut(t *testing.T, out map[string]any, key string) string {
	t.Helper()
	s, _ := out[key].(string)
	return s
}

// nodeValid reports whether the decoded validation outcome is valid. The API
// serializes `valid` as a JSON boolean, so it decodes to a Go bool.
func nodeValid(t *testing.T, out map[string]any) bool {
	t.Helper()
	v, _ := out["valid"].(bool)
	return v
}

func TestValidateProviderNode_RequiresBaseURLAndKey(t *testing.T) {
	calls := staticProbeStub(t, http.StatusOK)

	rec, out := postValidateNode(t, `{}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("missing fields: expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if nodeValid(t, out) {
		t.Fatalf("missing fields must not validate: %s", rec.Body.String())
	}
	if got := nodeOut(t, out, "error"); got != "Base URL and API key required" {
		t.Errorf("missing fields: unexpected error %q", got)
	}

	rec, out = postValidateNode(t, `{"baseUrl":"https://api.openai.com/v1"}`, true)
	if got := nodeOut(t, out, "error"); got != "Base URL and API key required" {
		t.Errorf("missing key: unexpected error %q", got)
	}

	rec, out = postValidateNode(t, `{"apiKey":"sk-test"}`, true)
	if got := nodeOut(t, out, "error"); got != "Base URL and API key required" {
		t.Errorf("missing baseUrl: unexpected error %q", got)
	}

	if len(*calls) != 0 {
		t.Errorf("expected no probe for invalid input, got %d", len(*calls))
	}
}

func TestValidateProviderNode_InvalidURLFormat(t *testing.T) {
	calls := staticProbeStub(t, http.StatusOK)

	rec, out := postValidateNode(t, `{"baseUrl":"not a url","apiKey":"k"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := nodeOut(t, out, "error"); got != "Invalid URL format" {
		t.Errorf("unexpected error %q", got)
	}
	if len(*calls) != 0 {
		t.Errorf("expected no probe, got %d", len(*calls))
	}
}

func TestValidateProviderNode_SSRFBlocksPrivateForRemoteCaller(t *testing.T) {
	calls := staticProbeStub(t, http.StatusOK)

	rec, out := postValidateNode(t, `{"baseUrl":"http://127.0.0.1:11434","apiKey":"k"}`, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := nodeOut(t, out, "error"); got != "URL not allowed" {
		t.Errorf("remote caller should be SSRF-blocked, got %q", got)
	}
	if len(*calls) != 0 {
		t.Errorf("expected no probe for blocked URL, got %d", len(*calls))
	}
}

func TestValidateProviderNode_LocalCallerAllowsSelfHostedNode(t *testing.T) {
	calls := staticProbeStub(t, http.StatusOK)

	rec, out := postValidateNode(t, `{"baseUrl":"http://localhost:11434","apiKey":"k","type":"openai-compatible"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !nodeValid(t, out) {
		t.Fatalf("self-hosted localhost node should validate locally, got %s", rec.Body.String())
	}
	if len(*calls) == 0 {
		t.Fatal("expected a models probe for the self-hosted node")
	}
}

func TestValidateProviderNode_OpenAIValid(t *testing.T) {
	calls := staticProbeStub(t, http.StatusOK)

	rec, out := postValidateNode(t, `{"baseUrl":"https://api.example.com/v1","apiKey":"sk-test","type":"openai-compatible"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !nodeValid(t, out) {
		t.Fatalf("expected valid, got %s", rec.Body.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("expected exactly one models probe, got %d", len(*calls))
	}
	if (*calls)[0].url != "https://api.example.com/v1/models" {
		t.Errorf("unexpected probe URL %q", (*calls)[0].url)
	}
	if got := (*calls)[0].headers.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("expected Bearer auth, got %q", got)
	}
}

func TestValidateProviderNode_OpenAIUnauthorized(t *testing.T) {
	calls := staticProbeStub(t, http.StatusUnauthorized)

	rec, out := postValidateNode(t, `{"baseUrl":"https://api.example.com/v1","apiKey":"bad","type":"openai-compatible"}`, true)
	if nodeValid(t, out) {
		t.Fatalf("401 must be invalid, got %s", rec.Body.String())
	}
	if got := nodeOut(t, out, "error"); got != "API key unauthorized" {
		t.Errorf("unexpected error %q", got)
	}
	if len(*calls) != 1 {
		t.Fatalf("401 must not fall back to chat, got %d probes", len(*calls))
	}
}

func TestValidateProviderNode_OpenAIModels404FallsBackToChat(t *testing.T) {
	calls := staticProbeStub(t, http.StatusNotFound, http.StatusOK)

	rec, out := postValidateNode(t, `{"baseUrl":"https://api.example.com/v1","apiKey":"k","type":"openai-compatible","modelId":"gpt-4"}`, true)
	if !nodeValid(t, out) {
		t.Fatalf("chat fallback should validate, got %s", rec.Body.String())
	}
	if got := nodeOut(t, out, "method"); got != "chat" {
		t.Errorf("expected method=chat, got %q", got)
	}
	if len(*calls) != 2 {
		t.Fatalf("expected models+chat probes, got %d", len(*calls))
	}
	if (*calls)[1].method != http.MethodPost || (*calls)[1].url != "https://api.example.com/v1/chat/completions" {
		t.Errorf("expected chat completions POST, got %s %s", (*calls)[1].method, (*calls)[1].url)
	}
	if !bytes.Contains([]byte((*calls)[1].body), []byte(`"model":"gpt-4"`)) {
		t.Errorf("chat probe must carry modelId, body=%s", (*calls)[1].body)
	}
}

// A bare-host node must be probed at the routes chat will actually call. The
// old probe appended "/models" and "/chat/completions" to the raw base, so a
// node the gateway serves correctly still failed its Check.
func TestValidateProviderNode_BareHostUsesTheChatRoute(t *testing.T) {
	calls := staticProbeStub(t, http.StatusNotFound, http.StatusOK)

	rec, out := postValidateNode(t, `{"baseUrl":"https://api.example.com","apiKey":"k","type":"openai-compatible","modelId":"gpt-4"}`, true)
	if !nodeValid(t, out) {
		t.Fatalf("chat fallback should validate, got %s", rec.Body.String())
	}
	if len(*calls) != 2 {
		t.Fatalf("expected models+chat probes, got %d", len(*calls))
	}
	if (*calls)[0].url != "https://api.example.com/v1/models" {
		t.Errorf("models probe = %q, want https://api.example.com/v1/models", (*calls)[0].url)
	}
	if (*calls)[1].url != "https://api.example.com/v1/chat/completions" {
		t.Errorf("chat probe = %q, want https://api.example.com/v1/chat/completions", (*calls)[1].url)
	}
}

func TestValidateProviderNode_OpenAIModels404WithoutModelID(t *testing.T) {
	calls := staticProbeStub(t, http.StatusNotFound)

	rec, out := postValidateNode(t, `{"baseUrl":"https://api.example.com/v1","apiKey":"k","type":"openai-compatible"}`, true)
	if nodeValid(t, out) {
		t.Fatalf("404 without modelId must be invalid, got %s", rec.Body.String())
	}
	if got := nodeOut(t, out, "error"); got != "/models endpoint not found - try chat validation with model ID" {
		t.Errorf("unexpected error %q", got)
	}
	if len(*calls) != 1 {
		t.Errorf("expected single models probe, got %d", len(*calls))
	}
}

func TestValidateProviderNode_AnthropicUsesXAPIKeyHeaders(t *testing.T) {
	calls := staticProbeStub(t, http.StatusOK)

	rec, out := postValidateNode(t, `{"baseUrl":"https://api.anthropic.com/v1","apiKey":"sk-ant-1","type":"anthropic-compatible"}`, true)
	if !nodeValid(t, out) {
		t.Fatalf("anthropic models 200 should validate, got %s", rec.Body.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("expected one models probe, got %d", len(*calls))
	}
	p := (*calls)[0]
	if got := p.headers.Get("x-api-key"); got != "sk-ant-1" {
		t.Errorf("expected x-api-key header, got %q", got)
	}
	if got := p.headers.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("expected anthropic-version header, got %q", got)
	}
	if got := p.headers.Get("Authorization"); got != "Bearer sk-ant-1" {
		t.Errorf("expected Bearer auth, got %q", got)
	}
}

func TestValidateProviderNode_AnthropicMessagesSuffixStripped(t *testing.T) {
	calls := staticProbeStub(t, http.StatusOK)

	postValidateNode(t, `{"baseUrl":"https://api.anthropic.com/v1/messages","apiKey":"k","type":"anthropic-compatible"}`, true)
	if len(*calls) == 0 {
		t.Fatal("expected a models probe")
	}
	// The /messages suffix must be stripped before probing /models.
	if (*calls)[0].url != "https://api.anthropic.com/v1/models" {
		t.Errorf("expected /v1/models probe, got %q", (*calls)[0].url)
	}
}

func TestValidateProviderNode_CustomEmbeddingReportsDimensions(t *testing.T) {
	embedBody := []byte(`{"data":[{"embedding":[0.1,0.2,0.3,0.4]}]}`)
	calls := withProbeStub(t, func(call int, _ capturedProbe) (int, []byte, error) {
		return http.StatusOK, embedBody, nil
	})

	rec, out := postValidateNode(t, `{"baseUrl":"https://embed.example.com","apiKey":"k","type":"custom-embedding","modelId":"text-embedding-3"}`, true)
	if !nodeValid(t, out) {
		t.Fatalf("embeddings 200 should validate, got %s", rec.Body.String())
	}
	if got := nodeOut(t, out, "method"); got != "embeddings" {
		t.Errorf("expected method=embeddings, got %q", got)
	}
	dims, _ := out["dimensions"].(float64)
	if dims != 4 {
		t.Errorf("expected dimensions=4, got %v", out["dimensions"])
	}
	if len(*calls) == 0 {
		t.Fatal("expected an embeddings probe")
	}
	if (*calls)[0].method != http.MethodPost || (*calls)[0].url != "https://embed.example.com/embeddings" {
		t.Errorf("expected POST /embeddings, got %s %s", (*calls)[0].method, (*calls)[0].url)
	}
	if !bytes.Contains([]byte((*calls)[0].body), []byte(`"model":"text-embedding-3"`)) {
		t.Errorf("embeddings probe must carry modelId, body=%s", (*calls)[0].body)
	}
}

func TestValidateProviderNode_NetworkErrorMapsToFriendlyMessage(t *testing.T) {
	calls := withProbeStub(t, func(call int, _ capturedProbe) (int, []byte, error) {
		return 0, nil, errConnRefused()
	})

	rec, out := postValidateNode(t, `{"baseUrl":"https://api.example.com/v1","apiKey":"k","type":"openai-compatible"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if nodeValid(t, out) {
		t.Fatalf("network error must be invalid, got %s", rec.Body.String())
	}
	if got := nodeOut(t, out, "error"); got != "Connection refused - provider node offline or unreachable" {
		t.Errorf("unexpected network error message %q", got)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected one probe, got %d", len(*calls))
	}
}
