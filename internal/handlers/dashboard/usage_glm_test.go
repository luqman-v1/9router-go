package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestGLMUsage_ParseAndFetch(t *testing.T) {
	sampleCreditJSON := `{
		"code": 200,
		"msg": "Operation successful",
		"data": {
			"limits": [
				{
					"type": "CREDIT_LIMIT",
					"unit": 3,
					"number": 5,
					"usage": 2000,
					"percentage": 25,
					"nextResetTime": 1787905548392
				},
				{
					"type": "CREDIT_LIMIT",
					"unit": 6,
					"number": 1,
					"percentage": 10,
					"nextResetTime": 1788492142997
				}
			],
			"level": "lite"
		},
		"success": true
	}`

	sampleTokensJSON := `{
		"code": 200,
		"msg": "Operation successful",
		"data": {
			"limits": [
				{
					"type": "TOKENS_LIMIT",
					"percentage": 40,
					"nextResetTime": 1787905548392
				}
			],
			"level": "standard"
		},
		"success": true
	}`

	t.Run("ParseCreditLimitResponse", func(t *testing.T) {
		var m map[string]any
		if err := json.Unmarshal([]byte(sampleCreditJSON), &m); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		res := parseGLMQuotaResponse(m)
		if res.plan != "Lite" {
			t.Errorf("plan = %q, want Lite", res.plan)
		}
		session, ok := res.quotas["Session (5h)"].(map[string]any)
		if !ok {
			t.Fatalf("missing Session (5h) quota: %+v", res.quotas)
		}
		if session["used"] != 25.0 {
			t.Errorf("used = %v, want 25", session["used"])
		}
		if session["remaining"] != 75.0 {
			t.Errorf("remaining = %v, want 75", session["remaining"])
		}
		weekly, ok := res.quotas["Weekly (7d)"].(map[string]any)
		if !ok {
			t.Fatalf("missing Weekly (7d) quota: %+v", res.quotas)
		}
		if weekly["used"] != 10.0 {
			t.Errorf("used = %v, want 10", weekly["used"])
		}
		if weekly["remaining"] != 90.0 {
			t.Errorf("remaining = %v, want 90", weekly["remaining"])
		}
	})

	t.Run("ParseTokensLimitResponse", func(t *testing.T) {
		var m map[string]any
		if err := json.Unmarshal([]byte(sampleTokensJSON), &m); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		res := parseGLMQuotaResponse(m)
		if res.plan != "Standard" {
			t.Errorf("plan = %q, want Standard", res.plan)
		}
		tokens, ok := res.quotas["Tokens"].(map[string]any)
		if !ok {
			t.Fatalf("missing Tokens quota: %+v", res.quotas)
		}
		if tokens["used"] != 40.0 {
			t.Errorf("used = %v, want 40", tokens["used"])
		}
		if tokens["remaining"] != 60.0 {
			t.Errorf("remaining = %v, want 60", tokens["remaining"])
		}
	})

	t.Run("LiveFetch_SuccessAndHeaders", func(t *testing.T) {
		var authHeader string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(sampleCreditJSON))
		}))
		defer server.Close()

		oldIntl := glmQuotaURLInternational
		glmQuotaURLInternational = server.URL
		defer func() { glmQuotaURLInternational = oldIntl }()

		res := fetchGLMUsage(context.Background(), "glm", "test-token-123")
		if authHeader != "Bearer test-token-123" {
			t.Errorf("Authorization = %q, want Bearer test-token-123", authHeader)
		}
		if res.plan != "Lite" {
			t.Errorf("plan = %q, want Lite", res.plan)
		}
	})

	t.Run("LiveFetch_Unauthorized401", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		oldIntl := glmQuotaURLInternational
		glmQuotaURLInternational = server.URL
		defer func() { glmQuotaURLInternational = oldIntl }()

		res := fetchGLMUsage(context.Background(), "glm", "bad-key")
		if res.message != "GLM API key invalid or expired." {
			t.Errorf("message = %q, want GLM API key invalid or expired.", res.message)
		}
	})

	t.Run("LiveFetch_MissingKey", func(t *testing.T) {
		res := fetchGLMUsage(context.Background(), "glm", "")
		if res.message != "GLM API key not available." {
			t.Errorf("message = %q, want GLM API key not available.", res.message)
		}
	})

	t.Run("LiveFetch_ChinaRegionURL", func(t *testing.T) {
		var requestedPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestedPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(sampleCreditJSON))
		}))
		defer server.Close()

		oldChina := glmQuotaURLChina
		glmQuotaURLChina = server.URL + "/cn-quota"
		defer func() { glmQuotaURLChina = oldChina }()

		_ = fetchGLMUsage(context.Background(), "glm-cn", "test-key")
		if requestedPath != "/cn-quota" {
			t.Errorf("requested path = %q, want /cn-quota", requestedPath)
		}
	})

	t.Run("IntegrationDispatchViaFetchProviderUsage", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(sampleTokensJSON))
		}))
		defer server.Close()

		oldIntl := glmQuotaURLInternational
		glmQuotaURLInternational = server.URL
		defer func() { glmQuotaURLInternational = oldIntl }()

		// Connection with apiKey only
		res, ok := fetchProviderUsage(context.Background(), "glm", map[string]any{
			"apiKey": "pasted-api-key",
		})
		if !ok || res.plan != "Standard" {
			t.Fatalf("expected ok=true and plan=Standard for apiKey, got ok=%v res=%+v", ok, res)
		}

		// Connection with accessToken only (OAuth-minted key)
		resOAuth, okOAuth := fetchProviderUsage(context.Background(), "glm", map[string]any{
			"accessToken": "oauth-plan-key",
		})
		if !okOAuth || resOAuth.plan != "Standard" {
			t.Fatalf("expected ok=true and plan=Standard for accessToken, got ok=%v res=%+v", okOAuth, resOAuth)
		}
	})
}

// Suppress unused reflect import if needed
var _ = reflect.TypeOf
