package chat

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAntigravityQuota_RefreshAndBlock(t *testing.T) {
	ClearAntigravityQuotaCache()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-ag-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		futureReset := time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339)
		resp := map[string]any{
			"models": map[string]any{
				"gemini-3.7-flash-high": map[string]any{
					"isInternal": false,
					"quotaInfo": map[string]any{
						"remainingFraction": 0.0,
						"resetTime":         futureReset,
					},
				},
				"gemini-3.5-flash-low": map[string]any{
					"isInternal": false,
					"quotaInfo": map[string]any{
						"remainingFraction": 0.85,
						"resetTime":         futureReset,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, resp)
	}))
	defer mockServer.Close()

	oldBase := antigravityQuotaBaseURL
	antigravityQuotaBaseURL = mockServer.URL
	defer func() { antigravityQuotaBaseURL = oldBase }()

	client := mockServer.Client()
	connectionID := "test-ag-conn-1"

	// 1. Initial refresh
	quotas, err := RefreshAntigravityQuota(context.Background(), client, connectionID, "test-ag-token", "test-project")
	if err != nil {
		t.Fatalf("RefreshAntigravityQuota failed: %v", err)
	}

	if len(quotas) != 2 {
		t.Errorf("expected 2 quotas, got %d", len(quotas))
	}

	// 2. Check model block for exhausted model (remaining=0)
	if !IsAntigravityModelBlocked(connectionID, "gemini-3.7-flash-high") {
		t.Errorf("expected gemini-3.7-flash-high to be blocked")
	}

	// 3. Check model block for available model (remaining=85)
	if IsAntigravityModelBlocked(connectionID, "gemini-3.5-flash-low") {
		t.Errorf("expected gemini-3.5-flash-low NOT to be blocked")
	}

	// 4. Handle 429 quota error returns resetAt
	resetAt := HandleAntigravityQuotaError(context.Background(), client, connectionID, 429, "gemini-3.7-flash-high", "test-ag-token", "test-project", "")
	if resetAt == nil || resetAt.Before(time.Now()) {
		t.Errorf("expected future resetAt from 429 handler, got %v", resetAt)
	}
}

func TestAntigravityQuota_CoalescingAndThrottle(t *testing.T) {
	ClearAntigravityQuotaCache()

	callCount := 0
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		time.Sleep(50 * time.Millisecond) // simulate delay
		resp := map[string]any{
			"models": map[string]any{
				"gemini-3.7-flash-high": map[string]any{
					"isInternal": false,
					"quotaInfo": map[string]any{
						"remainingFraction": 1.0,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, resp)
	}))
	defer mockServer.Close()

	oldBase := antigravityQuotaBaseURL
	antigravityQuotaBaseURL = mockServer.URL
	defer func() { antigravityQuotaBaseURL = oldBase }()

	client := mockServer.Client()
	connID := "test-ag-conn-throttle"

	// Concurrent refreshes should coalesce to 1 call
	done := make(chan bool)
	for i := 0; i < 5; i++ {
		go func() {
			_, _ = RefreshAntigravityQuota(context.Background(), client, connID, "tok", "proj")
			done <- true
		}()
	}
	for i := 0; i < 5; i++ {
		<-done
	}

	if callCount != 1 {
		t.Errorf("expected 1 call due to in-flight coalescing, got %d", callCount)
	}

	// Immediate next refresh should hit 30s throttle and not call upstream
	_, _ = RefreshAntigravityQuota(context.Background(), client, connID, "tok", "proj")
	if callCount != 1 {
		t.Errorf("expected callCount to stay 1 due to 30s throttle, got %d", callCount)
	}
}

func TestAntigravityWeeklyQuota_ParseAndFetch(t *testing.T) {
	rawSummary := []byte(`{
		"groups": [
			{
				"displayName": "Gemini Models",
				"buckets": [
					{
						"bucketId": "gemini-weekly-bucket",
						"displayName": "Gemini Weekly Limit",
						"disabled": false,
						"remainingFraction": 0.75,
						"resetTime": "2026-09-17T00:00:00Z"
					}
				]
			},
			{
				"displayName": "Claude & GPT Models",
				"buckets": [
					{
						"bucketId": "claude-gpt-weekly-bucket",
						"displayName": "Claude GPT Weekly",
						"disabled": false,
						"remainingFraction": 0.20,
						"resetTime": "2026-09-17T00:00:00Z"
					}
				]
			}
		]
	}`)

	parsed := ParseWeeklyQuotaSummary(rawSummary)
	if len(parsed) != 2 {
		t.Fatalf("expected 2 weekly quotas, got %d", len(parsed))
	}

	gw, ok := parsed["gemini_weekly"]
	if !ok {
		t.Fatal("expected gemini_weekly")
	}
	if gw.Total != 1000 || gw.Used != 250 || gw.RemainingPercentage != 75.0 {
		t.Errorf("unexpected gemini_weekly values: %+v", gw)
	}

	cw, ok := parsed["claude_gpt_weekly"]
	if !ok {
		t.Fatal("expected claude_gpt_weekly")
	}
	if cw.Total != 1000 || cw.Used != 800 || cw.RemainingPercentage != 20.0 {
		t.Errorf("unexpected claude_gpt_weekly values: %+v", cw)
	}

	// Test FetchAntigravityWeeklyQuota with mock server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-weekly-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(rawSummary)
	}))
	defer mockServer.Close()

	oldBase := antigravityQuotaBaseURL
	antigravityQuotaBaseURL = mockServer.URL
	defer func() { antigravityQuotaBaseURL = oldBase }()

	ClearAntigravityQuotaCache()
	res, err := FetchAntigravityWeeklyQuota(context.Background(), mockServer.Client(), "test-weekly-token", "proj-1")
	if err != nil {
		t.Fatalf("FetchAntigravityWeeklyQuota failed: %v", err)
	}
	if len(res) != 2 {
		t.Errorf("expected 2 quotas from fetch, got %d", len(res))
	}
}

// TestAntigravityWeeklyQuota_SessionBuckets is the Go port of
// decolua/9router#4209: 5h-session buckets parse separately from weekly
// buckets, disabled sessions pin to 0, and session exhaustion blocks routing.
func TestAntigravityWeeklyQuota_SessionBuckets(t *testing.T) {
	rawSummary := []byte(`{
		"groups": [
			{
				"displayName": "Gemini Models",
				"buckets": [
					{
						"bucketId": "gemini-weekly-bucket",
						"displayName": "Gemini Weekly Limit",
						"disabled": false,
						"remainingFraction": 0.75,
						"resetTime": "2099-01-01T00:00:00Z"
					},
					{
						"bucketId": "gemini-5h-bucket",
						"displayName": "Gemini Five Hour Limit",
						"window": "5h",
						"disabled": false,
						"remainingFraction": 0.40,
						"resetTime": "2099-01-01T05:00:00Z"
					},
					{
						"bucketId": "gemini-5h-disabled",
						"displayName": "Gemini 5h Limit",
						"disabled": true,
						"remainingFraction": 0.90,
						"resetTime": "2099-01-01T05:00:00Z"
					}
				]
			}
		]
	}`)

	parsed := ParseWeeklyQuotaSummary(rawSummary)
	if len(parsed) != 2 {
		t.Fatalf("expected weekly+session quotas, got %v", parsed)
	}
	gw, ok := parsed["gemini_weekly"]
	if !ok || gw.RemainingPercentage != 75.0 {
		t.Errorf("expected gemini_weekly at 75%%, got %+v", parsed)
	}
	// First session bucket wins; the disabled duplicate must not overwrite it.
	gs, ok := parsed["gemini_session"]
	if !ok {
		t.Fatal("expected gemini_session")
	}
	if gs.RemainingPercentage != 40.0 || gs.DisplayName != "Gemini (5h)" {
		t.Errorf("unexpected gemini_session values: %+v", gs)
	}

	// A lone disabled session bucket pins to 0 (upstream marks it disabled
	// when weekly is hit, but the row must still surface).
	disabledOnly := []byte(`{"groups": [{"displayName": "Gemini Models", "buckets": [
		{"bucketId": "gemini-5h-x", "displayName": "Gemini 5h", "disabled": true, "remainingFraction": 0.9}
	]}]}`)
	if ds := ParseWeeklyQuotaSummary(disabledOnly)["gemini_session"]; ds.RemainingPercentage != 0 {
		t.Errorf("expected disabled session pinned to 0, got %+v", ds)
	}

	// Session exhaustion blocks family routing even with healthy weekly/model quota.
	connID := "test-conn-session-block"
	future := time.Now().UTC().Add(2 * time.Hour)
	agQuotaMu.Lock()
	agQuotaCache[connID] = map[string]AntigravityModelQuota{
		"gemini-3.8-flash-x": {RemainingPercentage: 90, ResetAt: future},
		"gemini_weekly":      {RemainingPercentage: 75, ResetAt: future},
		"gemini_session":     {RemainingPercentage: 0, ResetAt: future},
	}
	agQuotaMu.Unlock()
	defer func() {
		agQuotaMu.Lock()
		delete(agQuotaCache, connID)
		agQuotaMu.Unlock()
	}()
	if !IsAntigravityModelBlocked(connID, "gemini-3.8-flash-x") {
		t.Errorf("expected block via exhausted gemini_session despite healthy weekly/model quota")
	}
}

// TestAntigravityQuota_Generic429NoStrike is the Go port of the
// decolua/9router#4197 regression test: generic/content-triggered 429s must
// not feed the strike-breaker while quota reads optimistic, while explicit
// quota 429s still block on the third strike.
func TestAntigravityQuota_Generic429NoStrike(t *testing.T) {
	const model = "test-strike-model"

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":{"` + model + `":{"quotaInfo":{"remainingFraction":0.9,"resetTime":"2099-01-01T00:00:00Z"}}}}`))
	}))
	defer mockServer.Close()

	oldBase := antigravityQuotaBaseURL
	antigravityQuotaBaseURL = mockServer.URL
	defer func() { antigravityQuotaBaseURL = oldBase }()

	ctx := context.Background()
	client := mockServer.Client()

	// 1. Generic content-triggered 429s never strike, even repeated.
	genericConn := "test-conn-generic-429"
	ClearAntigravityQuotaCache()
	ClearAntigravityStrikes(genericConn, model)
	genericErr := `{"error":{"code":429,"message":"Request blocked for content reasons.","status":"RESOURCE_EXHAUSTED"}}`
	for i := 0; i < 5; i++ {
		if res := HandleAntigravityQuotaError(ctx, client, genericConn, 429, model, "token", "proj", genericErr); res != nil {
			t.Fatalf("generic 429 #%d must not block, got %v", i+1, *res)
		}
	}
	if IsAntigravityModelBlocked(genericConn, model) {
		t.Errorf("generic 429s must not block the model while quota is optimistic")
	}

	// 2. Explicit quota 429s still strike: nil, nil, then block.
	quotaConn := "test-conn-quota-429"
	ClearAntigravityQuotaCache()
	ClearAntigravityStrikes(quotaConn, model)
	quotaErr := `{"error":{"code":429,"message":"Individual quota reached. Please upgrade your subscription.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`
	for i := 0; i < 2; i++ {
		if res := HandleAntigravityQuotaError(ctx, client, quotaConn, 429, model, "token", "proj", quotaErr); res != nil {
			t.Fatalf("explicit quota 429 #%d must not block yet, got %v", i+1, *res)
		}
	}
	blocked := HandleAntigravityQuotaError(ctx, client, quotaConn, 429, model, "token", "proj", quotaErr)
	if blocked == nil {
		t.Fatalf("explicit quota 429 #3 must trigger strike block")
	}
	if !IsAntigravityModelBlocked(quotaConn, model) {
		t.Errorf("model must be blocked after 3 explicit quota 429s")
	}
}
