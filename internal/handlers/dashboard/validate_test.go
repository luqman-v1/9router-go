package dashboard

import (
	"bytes"
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// capturedProbe records one stubbed outbound probe.
type capturedProbe struct {
	method  string
	url     string
	headers http.Header
	body    string
}

// withProbeStub replaces validateProbeDo for the duration of a test and records
// every probe. respond maps (call index, probe) to a status / body / error.
func withProbeStub(t *testing.T, respond func(call int, p capturedProbe) (int, []byte, error)) *[]capturedProbe {
	t.Helper()
	orig := validateProbeDo
	var calls []capturedProbe
	validateProbeDo = func(_ context.Context, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error) {
		h := http.Header{}
		for k, v := range headers {
			h.Set(k, v)
		}
		p := capturedProbe{method: method, url: rawURL, headers: h, body: string(body)}
		calls = append(calls, p)
		return respond(len(calls)-1, p)
	}
	t.Cleanup(func() { validateProbeDo = orig })
	return &calls
}

// staticProbeStub answers every probe with the given statuses in order, reusing
// the last status once the list is exhausted.
func staticProbeStub(t *testing.T, statuses ...int) *[]capturedProbe {
	t.Helper()
	return withProbeStub(t, func(call int, _ capturedProbe) (int, []byte, error) {
		if call >= len(statuses) {
			return statuses[len(statuses)-1], nil, nil
		}
		return statuses[call], nil, nil
	})
}

// errorMessage digs the message out of the standard error envelope
// ({"error":{"message":...,"code":...}}).
func errorMessage(t *testing.T, out map[string]any) string {
	t.Helper()
	switch e := out["error"].(type) {
	case string:
		return e
	case map[string]any:
		if msg, ok := e["message"].(string); ok {
			return msg
		}
	}
	return ""
}

func postValidate(t *testing.T, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	req := httptest.NewRequest(http.MethodPost, "/api/providers/validate", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var out map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

func TestHandleValidateProvider_RequiresProviderAndKey(t *testing.T) {
	calls := staticProbeStub(t, http.StatusOK)

	rec, out := postValidate(t, `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty body: expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	if got := errorMessage(t, out); got != "Provider and API key required" {
		t.Errorf("empty body: unexpected error %q", got)
	}

	rec, _ = postValidate(t, `{"provider":"clinepass"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing key: expected 400, got %d", rec.Code)
	}

	if len(*calls) != 0 {
		t.Errorf("expected no upstream probe for invalid requests, got %d", len(*calls))
	}
}

func TestHandleValidateProvider_UnknownProviderIsUnsupported(t *testing.T) {
	calls := staticProbeStub(t, http.StatusOK)

	rec, out := postValidate(t, `{"provider":"no-such-provider","apiKey":"k"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	if got := errorMessage(t, out); got != "Provider validation not supported" {
		t.Errorf("unexpected error %q", got)
	}
	if len(*calls) != 0 {
		t.Errorf("expected no upstream probe for unknown provider, got %d", len(*calls))
	}
}

func TestHandleValidateProvider_ClinepassProbesRegistryModelsEndpoint(t *testing.T) {
	calls := staticProbeStub(t, http.StatusOK)

	rec, out := postValidate(t, `{"provider":"clinepass","apiKey":"sk-test"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if out["valid"] != true {
		t.Errorf("expected valid=true, got %v", out["valid"])
	}
	if out["supported"] != true {
		t.Errorf("expected supported=true, got %v", out["supported"])
	}
	if out["error"] != nil {
		t.Errorf("expected null error, got %v", out["error"])
	}
	if len(*calls) != 1 {
		t.Fatalf("expected exactly one probe, got %d", len(*calls))
	}
	probe := (*calls)[0]
	if probe.method != http.MethodGet || probe.url != "https://api.cline.bot/api/v1/models" {
		t.Errorf("unexpected probe %s %s", probe.method, probe.url)
	}
	if got := probe.headers.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("expected bearer auth header, got %q", got)
	}
	// API-key probes carry only Accept + Authorization (upstream
	// buildModelListHeaders); the 9Router identity goes on OAuth probes.
	if got := probe.headers.Get("User-Agent"); got != "" {
		t.Errorf("expected no identity User-Agent on API-key probe, got %q", got)
	}
}

func TestHandleValidateProvider_AliasResolvesToCanonicalProvider(t *testing.T) {
	calls := staticProbeStub(t, http.StatusOK)

	rec, _ := postValidate(t, `{"provider":"cp","apiKey":"sk-test"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(*calls) != 1 || (*calls)[0].url != "https://api.cline.bot/api/v1/models" {
		t.Fatalf("alias cp did not resolve to clinepass: %+v", *calls)
	}
}

func TestHandleValidateProvider_InvalidKeyIsAValidResponse(t *testing.T) {
	staticProbeStub(t, http.StatusUnauthorized)

	rec, out := postValidate(t, `{"provider":"clinepass","apiKey":"nope"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid=false, got %d", rec.Code)
	}
	if out["valid"] != false {
		t.Errorf("expected valid=false, got %v", out["valid"])
	}
	if out["error"] != "Invalid API key" {
		t.Errorf("expected 'Invalid API key', got %v", out["error"])
	}
}

func TestHandleValidateProvider_FallsBackToChatProbe(t *testing.T) {
	calls := staticProbeStub(t, http.StatusNotFound, http.StatusBadRequest)

	rec, out := postValidate(t, `{"provider":"clinepass","apiKey":"sk-test"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(*calls) != 2 {
		t.Fatalf("expected models probe + chat fallback, got %d calls", len(*calls))
	}
	first, second := (*calls)[0], (*calls)[1]
	if first.url != "https://api.cline.bot/api/v1/models" {
		t.Errorf("unexpected first probe url %s", first.url)
	}
	if second.method != http.MethodPost || second.url != "https://api.cline.bot/api/v1/chat/completions" {
		t.Errorf("unexpected fallback probe %s %s", second.method, second.url)
	}
	if !strings.Contains(second.body, `"messages"`) {
		t.Errorf("fallback probe body missing chat payload: %s", second.body)
	}
	// 400 means auth passed (model resolution error), so the key is accepted.
	if out["valid"] != true {
		t.Errorf("expected valid=true after non-auth chat answer, got %v (%v)", out["valid"], out["error"])
	}
}

func TestHandleValidateProvider_AnthropicStyleProbe(t *testing.T) {
	calls := staticProbeStub(t, http.StatusBadRequest)

	rec, out := postValidate(t, `{"provider":"anthropic","apiKey":"sk-ant-test"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("expected one probe, got %d", len(*calls))
	}
	probe := (*calls)[0]
	if probe.method != http.MethodPost || probe.url != "https://api.anthropic.com/v1/messages" {
		t.Errorf("unexpected probe %s %s", probe.method, probe.url)
	}
	if got := probe.headers.Get("x-api-key"); got != "sk-ant-test" {
		t.Errorf("expected raw x-api-key header, got %q", got)
	}
	if got := probe.headers.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("expected anthropic-version header, got %q", got)
	}
	if out["valid"] != true {
		t.Errorf("expected valid=true for non-401 answer, got %v", out["valid"])
	}
}

func TestHandleValidateProvider_AzureRequiresProviderSpecificData(t *testing.T) {
	calls := staticProbeStub(t, http.StatusOK)

	rec, out := postValidate(t, `{"provider":"azure","apiKey":"az-key"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if out["valid"] != false || out["error"] != "Invalid API key or Azure configuration" {
		t.Errorf("expected azure config error, got valid=%v error=%v", out["valid"], out["error"])
	}
	if len(*calls) != 0 {
		t.Errorf("expected no probe without endpoint, got %d", len(*calls))
	}

	body := `{"provider":"azure","apiKey":"az-key","providerSpecificData":{` +
		`"azureEndpoint":"https://my-res.openai.azure.com/","deployment":"gpt-4","apiVersion":"2024-10-01-preview","organization":"org-1"}}`
	rec, out = postValidate(t, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("expected one probe, got %d", len(*calls))
	}
	probe := (*calls)[0]
	wantURL := "https://my-res.openai.azure.com/openai/deployments/gpt-4/chat/completions?api-version=2024-10-01-preview"
	if probe.url != wantURL {
		t.Errorf("unexpected azure url %s", probe.url)
	}
	if got := probe.headers.Get("api-key"); got != "az-key" {
		t.Errorf("expected api-key header, got %q", got)
	}
	if got := probe.headers.Get("OpenAI-Organization"); got != "org-1" {
		t.Errorf("expected organization header, got %q", got)
	}
	if out["valid"] != true {
		t.Errorf("expected valid=true, got %v", out["valid"])
	}
}

func TestHandleValidateProvider_CloudflareRequiresAccountID(t *testing.T) {
	calls := staticProbeStub(t, http.StatusOK)

	rec, out := postValidate(t, `{"provider":"cloudflare-ai","apiKey":"cf-token"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if out["valid"] != false || out["error"] != "Missing Account ID" {
		t.Errorf("expected Missing Account ID, got valid=%v error=%v", out["valid"], out["error"])
	}
	if len(*calls) != 0 {
		t.Errorf("expected no probe without account id, got %d", len(*calls))
	}

	rec, out = postValidate(t, `{"provider":"cloudflare-ai","apiKey":"cf-token","providerSpecificData":{"accountId":"acc123"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("expected one probe, got %d", len(*calls))
	}
	want := "https://api.cloudflare.com/client/v4/accounts/acc123/ai/v1/chat/completions"
	if (*calls)[0].url != want {
		t.Errorf("unexpected cloudflare url %s", (*calls)[0].url)
	}
	if out["valid"] != true {
		t.Errorf("expected valid=true, got %v", out["valid"])
	}
}

func TestHandleValidateProvider_OllamaLocalUsesHostFromProviderData(t *testing.T) {
	calls := staticProbeStub(t, http.StatusOK)

	rec, out := postValidate(t, `{"provider":"ollama-local","apiKey":"","providerSpecificData":{"baseUrl":"http://192.168.1.10:11434/"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("expected one probe, got %d", len(*calls))
	}
	probe := (*calls)[0]
	if probe.url != "http://192.168.1.10:11434/api/tags" {
		t.Errorf("unexpected ollama url %s", probe.url)
	}
	if probe.headers.Get("Authorization") != "" {
		t.Errorf("ollama-local probe should not send auth, got %q", probe.headers.Get("Authorization"))
	}
	if out["valid"] != true {
		t.Errorf("expected valid=true, got %v", out["valid"])
	}
}

func TestHandleValidateProvider_XiaomiTokenplanRegion(t *testing.T) {
	calls := staticProbeStub(t, http.StatusForbidden)

	rec, out := postValidate(t, `{"provider":"xiaomi-tokenplan","apiKey":"tp-1","providerSpecificData":{"region":"cn"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if len(*calls) != 1 || (*calls)[0].url != "https://token-plan-cn.xiaomimimo.com/v1/models" {
		t.Fatalf("unexpected probe for region cn: %+v", *calls)
	}
	// 403 means the key is valid but lacks list permission — upstream treats only 401 as fatal.
	if out["valid"] != true {
		t.Errorf("expected valid=true for 403, got %v", out["valid"])
	}

	staticProbeStub(t, http.StatusUnauthorized)
	_, out = postValidate(t, `{"provider":"xiaomi-tokenplan","apiKey":"tp-1"}`)
	if out["valid"] != false {
		t.Errorf("expected valid=false for 401, got %v", out["valid"])
	}
}

func TestHandleValidateProvider_NoAuthProviderSkipsProbe(t *testing.T) {
	calls := staticProbeStub(t, http.StatusInternalServerError)

	rec, out := postValidate(t, `{"provider":"comfyui","apiKey":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if out["valid"] != true {
		t.Errorf("expected valid=true for noAuth provider, got %v", out["valid"])
	}
	if len(*calls) != 0 {
		t.Errorf("expected no probe for noAuth provider, got %d", len(*calls))
	}
}

func TestHandleValidateProvider_ProbeFailureIsReportedAsInvalid(t *testing.T) {
	withProbeStub(t, func(_ int, _ capturedProbe) (int, []byte, error) {
		return 0, nil, context.DeadlineExceeded
	})

	rec, out := postValidate(t, `{"provider":"clinepass","apiKey":"sk-test"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if out["valid"] != false {
		t.Errorf("expected valid=false on probe error, got %v", out["valid"])
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "deadline exceeded") {
		t.Errorf("expected probe error surfaced, got %v", out["error"])
	}
}

// --- grok-web ---

func TestHandleValidateProvider_GrokWebCookieProbe(t *testing.T) {
	calls := staticProbeStub(t, http.StatusOK)

	rec, out := postValidate(t, `{"provider":"grok-web","apiKey":"sso=sso-token-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("expected one probe, got %d", len(*calls))
	}
	probe := (*calls)[0]
	if probe.method != http.MethodPost || probe.url != "https://grok.com/rest/app-chat/conversations/new" {
		t.Errorf("unexpected probe %s %s", probe.method, probe.url)
	}
	// The `sso=` prefix must be stripped, not doubled.
	if got := probe.headers.Get("Cookie"); got != "sso=sso-token-1" {
		t.Errorf("unexpected cookie header %q", got)
	}
	wantStatsig := base64.StdEncoding.EncodeToString([]byte("e:TypeError: Cannot read properties of null (reading 'children')"))
	if got := probe.headers.Get("x-statsig-id"); got != wantStatsig {
		t.Errorf("unexpected x-statsig-id %q", got)
	}
	if got := probe.headers.Get("traceparent"); !regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-00$`).MatchString(got) {
		t.Errorf("unexpected traceparent %q", got)
	}
	if !strings.Contains(probe.body, `"modelMode":"MODEL_MODE_GROK_4"`) {
		t.Errorf("probe body missing grok payload: %s", probe.body)
	}
	if out["valid"] != true {
		t.Errorf("expected valid=true, got %v", out["valid"])
	}
}

func TestHandleValidateProvider_GrokWebRejectsBadCookie(t *testing.T) {
	staticProbeStub(t, http.StatusForbidden)

	_, out := postValidate(t, `{"provider":"grok-web","apiKey":"raw-token"}`)
	if out["valid"] != false {
		t.Errorf("expected valid=false for 403, got %v", out["valid"])
	}
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, "Invalid SSO cookie") {
		t.Errorf("expected SSO guidance, got %v", out["error"])
	}
}

// --- perplexity-web ---

func TestHandleValidateProvider_PerplexityWebSessionProbe(t *testing.T) {
	calls := staticProbeStub(t, http.StatusTooManyRequests)

	rec, out := postValidate(t, `{"provider":"perplexity-web","apiKey":"__Secure-next-auth.session-token=sess-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("expected one probe, got %d", len(*calls))
	}
	probe := (*calls)[0]
	if probe.method != http.MethodPost || probe.url != "https://www.perplexity.ai/rest/sse/perplexity_ask" {
		t.Errorf("unexpected probe %s %s", probe.method, probe.url)
	}
	if got := probe.headers.Get("Cookie"); got != "__Secure-next-auth.session-token=sess-1" {
		t.Errorf("unexpected cookie header %q", got)
	}
	if got := probe.headers.Get("X-App-ApiVersion"); got != "2.18" {
		t.Errorf("unexpected X-App-ApiVersion %q", got)
	}
	if !strings.Contains(probe.body, `"query_str":"ping"`) {
		t.Errorf("probe body missing query: %s", probe.body)
	}
	// 429 means the cookie was accepted.
	if out["valid"] != true {
		t.Errorf("expected valid=true for 429, got %v (%v)", out["valid"], out["error"])
	}
}

func TestHandleValidateProvider_PerplexityWebRejectsBadCookie(t *testing.T) {
	staticProbeStub(t, http.StatusUnauthorized)

	_, out := postValidate(t, `{"provider":"perplexity-web","apiKey":"sess-bad"}`)
	if out["valid"] != false {
		t.Errorf("expected valid=false for 401, got %v", out["valid"])
	}
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, "Invalid session cookie") {
		t.Errorf("expected session cookie guidance, got %v", out["error"])
	}
}

// --- qoder ---

// qoderProbeStub answers the PAT exchange, userinfo and model list by URL.
func qoderProbeStub(t *testing.T, listStatus int, listBody string) *[]capturedProbe {
	t.Helper()
	return withProbeStub(t, func(_ int, p capturedProbe) (int, []byte, error) {
		switch {
		case strings.HasSuffix(p.url, "/jobToken/exchange"):
			return http.StatusOK, []byte(`{"token":"jt-abc"}`), nil
		case strings.HasSuffix(p.url, "/userinfo"):
			return http.StatusOK, []byte(`{"id":"user-1"}`), nil
		case strings.HasSuffix(p.url, "/model/list"):
			return listStatus, []byte(listBody), nil
		}
		return http.StatusNotFound, nil, nil
	})
}

func TestHandleValidateProvider_QoderPATExchangesAndListsModels(t *testing.T) {
	calls := qoderProbeStub(t, http.StatusOK, `{"chat":[{"key":"auto","enable":true},{"key":"hidden","enable":false}]}`)

	rec, out := postValidate(t, `{"provider":"qoder","apiKey":"pt-personal-token"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(*calls) != 3 {
		t.Fatalf("expected exchange + userinfo + model list, got %d calls: %+v", len(*calls), *calls)
	}
	exchange := (*calls)[0]
	if exchange.url != "https://openapi.qoder.sh/api/v1/jobToken/exchange" || exchange.method != http.MethodPost {
		t.Errorf("unexpected exchange probe %s %s", exchange.method, exchange.url)
	}
	if !strings.Contains(exchange.body, `"personal_token":"pt-personal-token"`) {
		t.Errorf("exchange body missing PAT: %s", exchange.body)
	}
	if got := exchange.headers.Get("User-Agent"); got != "qodercli/1.0.0" {
		t.Errorf("unexpected exchange user agent %q", got)
	}

	userinfo := (*calls)[1]
	if userinfo.url != "https://openapi.qoder.sh/api/v1/userinfo" {
		t.Errorf("unexpected userinfo probe %s", userinfo.url)
	}
	if got := userinfo.headers.Get("Authorization"); got != "Bearer jt-abc" {
		t.Errorf("expected job token on userinfo, got %q", got)
	}

	// Job tokens must hit api2 — api3 answers 403 "Login expired".
	list := (*calls)[2]
	if list.url != "https://api2.qoder.sh/algo/api/v2/model/list" {
		t.Errorf("unexpected model list url %s", list.url)
	}
	if got := list.headers.Get("Cosy-User"); got != "user-1" {
		t.Errorf("expected COSY user header, got %q", got)
	}
	if got := list.headers.Get("Cosy-Sigpath"); got != "/api/v2/model/list" {
		t.Errorf("expected sigpath /api/v2/model/list, got %q", got)
	}
	if !strings.HasPrefix(list.headers.Get("Authorization"), "Bearer COSY.") {
		t.Errorf("expected COSY authorization header, got %q", list.headers.Get("Authorization"))
	}
	if out["valid"] != true {
		t.Errorf("expected valid=true, got %v (%v)", out["valid"], out["error"])
	}
}

func TestHandleValidateProvider_QoderNonPATStaysOnApi3(t *testing.T) {
	calls := qoderProbeStub(t, http.StatusOK, `{"chat":[{"key":"auto"}]}`)

	_, out := postValidate(t, `{"provider":"qoder","apiKey":"dt-device-token","providerSpecificData":{"userId":"user-9"}}`)
	if out["valid"] != true {
		t.Fatalf("expected valid=true, got %v (%v)", out["valid"], out["error"])
	}
	// Device tokens skip the exchange entirely.
	if len(*calls) != 1 {
		t.Fatalf("expected only the model list probe, got %d: %+v", len(*calls), *calls)
	}
	if (*calls)[0].url != "https://api3.qoder.sh/algo/api/v2/model/list" {
		t.Errorf("unexpected model list url %s", (*calls)[0].url)
	}
}

func TestHandleValidateProvider_QoderPATExchangeFailure(t *testing.T) {
	withProbeStub(t, func(_ int, _ capturedProbe) (int, []byte, error) {
		return http.StatusUnauthorized, []byte(`{"message":"bad token"}`), nil
	})

	_, out := postValidate(t, `{"provider":"qoder","apiKey":"pt-bad"}`)
	if out["valid"] != false {
		t.Errorf("expected valid=false, got %v", out["valid"])
	}
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, "PAT exchange failed") {
		t.Errorf("expected PAT exchange error, got %v", out["error"])
	}
}

func TestHandleValidateProvider_QoderWithoutUserIDOrModels(t *testing.T) {
	// PAT exchange works but userinfo has no id → COSY cannot be signed.
	withProbeStub(t, func(_ int, p capturedProbe) (int, []byte, error) {
		switch {
		case strings.HasSuffix(p.url, "/jobToken/exchange"):
			return http.StatusOK, []byte(`{"token":"jt-abc"}`), nil
		case strings.HasSuffix(p.url, "/userinfo"):
			return http.StatusOK, []byte(`{}`), nil
		}
		return http.StatusOK, []byte(`{"chat":[]}`), nil
	})
	_, out := postValidate(t, `{"provider":"qoder","apiKey":"pt-x"}`)
	if out["valid"] != false {
		t.Errorf("expected valid=false without user id, got %v", out["valid"])
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "user ID missing") {
		t.Errorf("expected user id guidance, got %v", out["error"])
	}

	// Credential resolves but the catalog has no enabled models.
	calls := qoderProbeStub(t, http.StatusOK, `{"chat":[{"key":"hidden","enable":false}]}`)
	out2 := func() map[string]any { _, o := postValidate(t, `{"provider":"qoder","apiKey":"pt-y"}`); return o }()
	if out2["valid"] != false {
		t.Errorf("expected valid=false when every model is disabled, got %v", out2["valid"])
	}
	if msg, _ := out2["error"].(string); !strings.Contains(msg, "no models") {
		t.Errorf("expected no-models message, got %v", out2["error"])
	}
	if len(*calls) != 3 {
		t.Errorf("expected full qoder chain, got %d calls", len(*calls))
	}
}

// --- iflow (generic OpenAI-compatible path) ---

func TestHandleValidateProvider_IflowUsesGenericProbe(t *testing.T) {
	calls := staticProbeStub(t, http.StatusOK)

	rec, out := postValidate(t, `{"provider":"iflow","apiKey":"iflow-key"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("expected one probe, got %d", len(*calls))
	}
	if (*calls)[0].url != "https://apis.iflow.cn/v1/models" {
		t.Errorf("unexpected iflow probe url %s", (*calls)[0].url)
	}
	if got := (*calls)[0].headers.Get("Authorization"); got != "Bearer iflow-key" {
		t.Errorf("expected bearer auth, got %q", got)
	}
	if out["valid"] != true {
		t.Errorf("expected valid=true, got %v", out["valid"])
	}
}

func TestHandleValidateProvider_CustomOpenAINode(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	nodeID := "openai-compatible-chat-custom123"
	name := "Custom Node"
	_, err := repo.CreateProviderNode(nodeID, "openai-compatible", name, `{"baseUrl":"https://my-custom-llm.com/v1","prefix":"cllm"}`)
	if err != nil {
		t.Fatalf("failed to create node: %v", err)
	}

	calls := staticProbeStub(t, http.StatusOK)

	body := `{"provider":"` + nodeID + `","apiKey":"my-secret-key"}`
	req := httptest.NewRequest(http.MethodPost, "/api/providers/validate", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(*calls) != 2 {
		t.Fatalf("expected 2 probes (models + chat), got %d", len(*calls))
	}
	if (*calls)[0].url != "https://my-custom-llm.com/v1/models" {
		t.Errorf("unexpected probe url %s", (*calls)[0].url)
	}
	if (*calls)[1].url != "https://my-custom-llm.com/v1/chat/completions" {
		t.Errorf("unexpected probe url %s", (*calls)[1].url)
	}
	if got := (*calls)[0].headers.Get("Authorization"); got != "Bearer my-secret-key" {
		t.Errorf("expected bearer auth, got %q", got)
	}
	if got := (*calls)[1].headers.Get("Authorization"); got != "Bearer my-secret-key" {
		t.Errorf("expected bearer auth, got %q", got)
	}

	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["valid"] != true {
		t.Errorf("expected valid=true, got %v", out["valid"])
	}
	if out["supported"] != true {
		t.Errorf("expected supported=true, got %v", out["supported"])
	}
}

func TestHandleValidateProvider_CustomOpenAINode_InvalidKeyRejection(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	nodeID := "openai-compatible-chat-public-models"
	name := "Public Models Node"
	_, err := repo.CreateProviderNode(nodeID, "openai-compatible", name, `{"baseUrl":"https://public-models-llm.com/v1","prefix":"pm"}`)
	if err != nil {
		t.Fatalf("failed to create node: %v", err)
	}

	// /models returns 200 (public), but /chat/completions returns 401 (invalid key rejected)
	calls := withProbeStub(t, func(call int, p capturedProbe) (int, []byte, error) {
		if strings.HasSuffix(p.url, "/models") {
			return http.StatusOK, []byte(`{"data":[{"id":"test-model"}]}`), nil
		}
		return http.StatusUnauthorized, []byte(`{"error":{"message":"invalid token"}}`), nil
	})

	body := `{"provider":"` + nodeID + `","apiKey":"fake-random-key"}`
	req := httptest.NewRequest(http.MethodPost, "/api/providers/validate", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(*calls) != 2 {
		t.Fatalf("expected 2 probes, got %d", len(*calls))
	}

	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["valid"] != false {
		t.Errorf("expected valid=false when chat returns 401, got %v", out["valid"])
	}
	if out["error"] != "Invalid API key" {
		t.Errorf("expected 'Invalid API key', got %v", out["error"])
	}
}

// TestHandleValidateProvider_CustomAnthropicNodeProbeModel pins which model the
// node validation path probes with: the shared current-model constant when the
// node names none, and the node's own assignedModel when it does. The retired
// claude-3-haiku-20240307 upstream hardcoded here is a valid answer to reject
// — a probe naming it fails for a perfectly good key.
func TestHandleValidateProvider_CustomAnthropicNodeProbeModel(t *testing.T) {
	const nodeID = "anthropic-compatible-node-probe"

	tests := []struct {
		name string
		psd  string
		want string
	}{
		{
			name: "no assigned model falls back to the shared constant",
			psd:  "",
			want: AnthropicValidationModel,
		},
		{
			name: "an assigned model wins over the constant",
			psd:  `,"providerSpecificData":{"assignedModel":"ac/claude-sonnet-4-6"}`,
			want: "ac/claude-sonnet-4-6",
		},
		{
			name: "a blank assigned model does not win",
			psd:  `,"providerSpecificData":{"assignedModel":"   "}`,
			want: AnthropicValidationModel,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo, cleanup := setupTestDB(t)
			defer cleanup()
			router := setupTestRouter(repo)
			if _, err := repo.CreateProviderNode(nodeID, "anthropic-compatible", "AC",
				`{"baseUrl":"https://ac.example/v1","prefix":"ac"}`); err != nil {
				t.Fatalf("seed node: %v", err)
			}
			calls := staticProbeStub(t, http.StatusOK)

			body := `{"provider":"` + nodeID + `","apiKey":"sk-ant"` + tc.psd + `}`
			req := httptest.NewRequest(http.MethodPost, "/api/providers/validate", bytes.NewReader([]byte(body)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
			}
			if len(*calls) == 0 {
				t.Fatal("expected a messages probe")
			}
			var sent struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal([]byte((*calls)[0].body), &sent); err != nil {
				t.Fatalf("probe body not JSON: %v", err)
			}
			if sent.Model != tc.want {
				t.Errorf("probe model = %q, want %q", sent.Model, tc.want)
			}
		})
	}
}

func TestHandleValidateProvider_Nous(t *testing.T) {
	t.Run("valid key probes with laguna-xs-2.1:free", func(t *testing.T) {
		calls := staticProbeStub(t, http.StatusOK)
		rec, out := postValidate(t, `{"provider":"nous","apiKey":"portal-key"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if out["valid"] != true {
			t.Fatalf("expected valid=true, got %v", out)
		}
		if len(*calls) != 1 {
			t.Fatalf("expected 1 probe call, got %d", len(*calls))
		}
		call := (*calls)[0]
		if call.url != "https://inference-api.nousresearch.com/v1/chat/completions" {
			t.Errorf("expected URL https://inference-api.nousresearch.com/v1/chat/completions, got %q", call.url)
		}
		if call.method != http.MethodPost {
			t.Errorf("expected POST, got %q", call.method)
		}
		if got := call.headers.Get("Authorization"); got != "Bearer portal-key" {
			t.Errorf("expected Authorization Bearer portal-key, got %q", got)
		}
		var reqBody struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			Stream    bool   `json:"stream"`
		}
		if err := json.Unmarshal([]byte(call.body), &reqBody); err != nil {
			t.Fatalf("failed to unmarshal probe body: %v", err)
		}
		if reqBody.Model != "poolside/laguna-xs-2.1:free" {
			t.Errorf("expected model poolside/laguna-xs-2.1:free, got %q", reqBody.Model)
		}
		if reqBody.MaxTokens != 1 || reqBody.Stream != false {
			t.Errorf("unexpected probe body parameters: %+v", reqBody)
		}
	})

	t.Run("rejected key returns Invalid or inactive API key", func(t *testing.T) {
		staticProbeStub(t, http.StatusUnauthorized)
		rec, out := postValidate(t, `{"provider":"nous","apiKey":"bad-key"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if out["valid"] != false {
			t.Errorf("expected valid=false, got %v", out)
		}
		if out["error"] != "Invalid or inactive API key" {
			t.Errorf("expected 'Invalid or inactive API key', got %v", out["error"])
		}
	})

	t.Run("inconclusive non-200 returns status error", func(t *testing.T) {
		staticProbeStub(t, http.StatusPaymentRequired)
		rec, out := postValidate(t, `{"provider":"nous","apiKey":"key"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if out["valid"] != false {
			t.Errorf("expected valid=false, got %v", out)
		}
		if out["error"] != "Unable to verify API key (Nous returned 402)" {
			t.Errorf("expected 'Unable to verify API key (Nous returned 402)', got %v", out["error"])
		}
	})
}

// --- runcrate ---

func TestHandleValidateProvider_RuncrateProbesBillingBalance(t *testing.T) {
	calls := staticProbeStub(t, http.StatusOK)

	rec, out := postValidate(t, `{"provider":"runcrate","apiKey":"rc-test"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if out["valid"] != true {
		t.Errorf("expected valid=true, got %v", out["valid"])
	}
	if out["supported"] != true {
		t.Errorf("expected supported=true, got %v", out["supported"])
	}
	if out["error"] != nil {
		t.Errorf("expected null error, got %v", out["error"])
	}
	if len(*calls) != 1 {
		t.Fatalf("expected exactly one probe, got %d", len(*calls))
	}
	probe := (*calls)[0]
	if probe.method != http.MethodGet || probe.url != "https://www.runcrate.ai/api/v1/billing/balance" {
		t.Errorf("unexpected probe %s %s", probe.method, probe.url)
	}
	if got := probe.headers.Get("Authorization"); got != "Bearer rc-test" {
		t.Errorf("expected bearer auth header, got %q", got)
	}
}

func TestHandleValidateProvider_RuncrateAliasResolves(t *testing.T) {
	calls := staticProbeStub(t, http.StatusOK)

	rec, _ := postValidate(t, `{"provider":"rc","apiKey":"rc-test"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(*calls) != 1 || (*calls)[0].url != "https://www.runcrate.ai/api/v1/billing/balance" {
		t.Fatalf("alias rc did not resolve to runcrate: %+v", *calls)
	}
}

func TestHandleValidateProvider_RuncrateInvalidKey(t *testing.T) {
	staticProbeStub(t, http.StatusUnauthorized)

	rec, out := postValidate(t, `{"provider":"runcrate","apiKey":"nope"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid=false, got %d", rec.Code)
	}
	if out["valid"] != false {
		t.Errorf("expected valid=false, got %v", out["valid"])
	}
	if out["error"] != "Invalid API key" {
		t.Errorf("expected 'Invalid API key', got %v", out["error"])
	}
}
