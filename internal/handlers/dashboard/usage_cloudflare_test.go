package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func stubCloudflareGraphQL(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	prev := cloudflareAIUsageURL
	cloudflareAIUsageURL = srv.URL
	t.Cleanup(func() { cloudflareAIUsageURL = prev })
}

func cloudflareGroupsResponse(groups ...map[string]any) map[string]any {
	return map[string]any{
		"data": map[string]any{
			"viewer": map[string]any{
				"accounts": []any{
					map[string]any{"aiInferenceAdaptiveGroups": groups},
				},
			},
		},
	}
}

func TestFetchCloudflareAiUsageSuccess(t *testing.T) {
	var body map[string]any
	var auth string
	stubCloudflareGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cloudflareGroupsResponse(
			map[string]any{"count": 7, "sum": map[string]any{"totalNeurons": 1200}},
			map[string]any{"count": 5, "sum": map[string]any{"totalNeurons": 321}},
		))
	})

	res := fetchCloudflareAiUsage(context.Background(), " cf-token ", map[string]any{"accountId": " acct-123 "})
	if res.message != "" {
		t.Fatalf("unexpected message: %s", res.message)
	}

	if auth != "Bearer cf-token" {
		t.Errorf("Authorization = %q, want Bearer cf-token", auth)
	}
	query, _ := body["query"].(string)
	if !strings.Contains(query, "aiInferenceAdaptiveGroups") || !strings.Contains(query, "totalNeurons") {
		t.Errorf("query missing analytics dataset: %q", query)
	}
	if strings.Contains(query, "acct-123") {
		t.Errorf("account id must be a variable, not spliced into query: %q", query)
	}
	vars, _ := body["variables"].(map[string]any)
	if vars["accountTag"] != "acct-123" {
		t.Errorf("variables.accountTag = %v", vars["accountTag"])
	}
	today := time.Now().UTC().Format("2006-01-02")
	if vars["since"] != today {
		t.Errorf("variables.since = %v, want %s", vars["since"], today)
	}

	neurons, ok := res.quotas["neurons"].(map[string]any)
	if !ok {
		t.Fatalf("neurons quota missing: %#v", res.quotas)
	}
	if neurons["used"] != float64(1521) {
		t.Errorf("neurons.used = %v, want 1521", neurons["used"])
	}
	if neurons["total"] != float64(10000) {
		t.Errorf("neurons.total = %v, want 10000", neurons["total"])
	}
	if _, ok := neurons["remaining"]; ok {
		t.Errorf("neurons must not emit absolute remaining (UI reads it as a percentage): %v", neurons["remaining"])
	}
	if neurons["remainingPercentage"] != 84.79 {
		t.Errorf("neurons.remainingPercentage = %v, want 84.79", neurons["remainingPercentage"])
	}
	if neurons["unlimited"] != false {
		t.Errorf("neurons.unlimited = %v, want false", neurons["unlimited"])
	}
	if neurons["name"] != "Neurons (daily)" {
		t.Errorf("neurons.name = %v", neurons["name"])
	}
	resetAt, _ := neurons["resetAt"].(string)
	wantReset := time.Now().UTC().AddDate(0, 0, 1).Truncate(24 * time.Hour).Format(time.RFC3339)
	if resetAt != wantReset {
		t.Errorf("resetAt = %q, want %q", resetAt, wantReset)
	}

	requests, ok := res.quotas["requests"].(map[string]any)
	if !ok {
		t.Fatalf("requests quota missing: %#v", res.quotas)
	}
	if requests["used"] != float64(12) {
		t.Errorf("requests.used = %v, want 12", requests["used"])
	}
	if requests["unlimited"] != true {
		t.Errorf("requests.unlimited = %v, want true", requests["unlimited"])
	}
	if !strings.Contains(res.plan, "best-effort") {
		t.Errorf("plan = %q, want best-effort label", res.plan)
	}
	if res.extra["resetDate"] != resetAt {
		t.Errorf("extra.resetDate = %v, want %q", res.extra["resetDate"], resetAt)
	}
}

func TestFetchCloudflareAiUsageMissingCredentials(t *testing.T) {
	calls := 0
	stubCloudflareGraphQL(t, func(w http.ResponseWriter, r *http.Request) { calls++ })

	noAccount := fetchCloudflareAiUsage(context.Background(), "token", map[string]any{})
	if !strings.Contains(noAccount.message, "Account ID missing") {
		t.Errorf("missing account message = %q", noAccount.message)
	}
	noToken := fetchCloudflareAiUsage(context.Background(), "", map[string]any{"accountId": "acct"})
	if !strings.Contains(noToken.message, "API token not available") {
		t.Errorf("missing token message = %q", noToken.message)
	}
	if calls != 0 {
		t.Errorf("made %d network calls with missing credentials", calls)
	}
}

func TestFetchCloudflareAiUsageAuthFailures(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			stubCloudflareGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			})
			res := fetchCloudflareAiUsage(context.Background(), "token", map[string]any{"accountId": "acct"})
			if res.message != cloudflareAIAnalyticsPermissionMessage {
				t.Errorf("message = %q, want permission message", res.message)
			}
		})
	}
}

func TestFetchCloudflareAiUsageGraphQLErrors(t *testing.T) {
	t.Run("auth error", func(t *testing.T) {
		stubCloudflareGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errors": []any{map[string]any{"message": "Not authorized to access analytics"}},
			})
		})
		res := fetchCloudflareAiUsage(context.Background(), "token", map[string]any{"accountId": "acct"})
		if res.message != cloudflareAIAnalyticsPermissionMessage {
			t.Errorf("message = %q, want permission message", res.message)
		}
	})
	t.Run("other error", func(t *testing.T) {
		stubCloudflareGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errors": []any{map[string]any{"message": "dataset temporarily unavailable"}},
			})
		})
		res := fetchCloudflareAiUsage(context.Background(), "token", map[string]any{"accountId": "acct"})
		if !strings.Contains(res.message, "dataset temporarily unavailable") {
			t.Errorf("message = %q", res.message)
		}
	})
}

func TestFetchCloudflareAiUsageLimitOverride(t *testing.T) {
	stubCloudflareGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cloudflareGroupsResponse(
			map[string]any{"count": 1, "sum": map[string]any{"totalNeurons": 250}},
		))
	})
	res := fetchCloudflareAiUsage(context.Background(), "token", map[string]any{
		"accountId":        "acct",
		"dailyNeuronLimit": float64(2000),
	})
	neurons := res.quotas["neurons"].(map[string]any)
	if neurons["total"] != float64(2000) || neurons["remainingPercentage"] != 87.5 {
		t.Errorf("override quota = %#v", neurons)
	}
	if !strings.Contains(res.plan, "2,000 neurons/day") {
		t.Errorf("plan = %q, want 2,000 label", res.plan)
	}
}

func TestCloudflareAiUsageRegistered(t *testing.T) {
	if !strSliceContains(usageSupportedProviders, "cloudflare-ai") {
		t.Error("cloudflare-ai missing from usageSupportedProviders")
	}
	if !strSliceContains(usageApikeyProviders, "cloudflare-ai") {
		t.Error("cloudflare-ai missing from usageApikeyProviders")
	}
	stubCloudflareGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cloudflareGroupsResponse())
	})
	res, ok := fetchProviderUsage(context.Background(), "cloudflare-ai", map[string]any{
		"apiKey":               "token",
		"providerSpecificData": map[string]any{"accountId": "acct"},
	})
	if !ok {
		t.Fatal("fetchProviderUsage returned ok=false for cloudflare-ai")
	}
	if res.message != "" {
		t.Errorf("unexpected message: %s", res.message)
	}
	if _, ok := res.quotas["neurons"]; !ok {
		t.Error("dispatcher result missing neurons quota")
	}
}
