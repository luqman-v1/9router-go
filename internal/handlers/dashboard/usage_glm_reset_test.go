package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// setupTestRouterForGLM creates a test router with the GLM reset cards routes mounted.
func setupTestRouterForGLM(repo *db.Repo) chi.Router {
	r := chi.NewRouter()
	h := NewDashboardHandler(repo)
	// Mount the GLM reset cards routes directly (they're registered in RegisterRoutes)
	RegisterRoutes(r, h)
	return r
}

// wrapZaiResponse wraps the payload in Z.ai response format.
func wrapZaiResponse(data any) map[string]any {
	return map[string]any{
		"success": true,
		"code":    200,
		"data":    data,
	}
}

// TestHandleGetGLMResetCards_Success tests successful retrieval of GLM reset cards.
func TestHandleGetGLMResetCards_Success(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	router := setupTestRouterForGLM(repo)

	// Create a GLM connection using the full method
	connID := "test-glm-conn-1"
	if err := repo.CreateProviderConnectionFull(connID, "glm", "api_key", "Test GLM", nil, `{"apiKey":"test-glm-key"}`); err != nil {
		t.Fatalf("failed to create connection: %v", err)
	}

	// Create a test server that mocks the GLM reset cards API
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/biz/customer-package-reset/list" && r.URL.Query().Get("targetType") == "PERSONAL" {
			w.Header().Set("Content-Type", "application/json")
			response := wrapZaiResponse(map[string]any{
				"fiveHourResets": []any{
					map[string]any{"recordId": "card-1", "available": true, "expireTime": "2026-12-31 23:59:59"},
				},
				"weekResets": []any{
					map[string]any{"recordId": "card-2", "available": true, "expireTime": "2027-01-15T23:59:59Z"},
				},
			})
			json.NewEncoder(w).Encode(response)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	// Override the GLM reset list URL for testing
	originalURL := glmResetListURL
	glmResetListURL = mockServer.URL + "/api/biz/customer-package-reset/list"
	defer func() { glmResetListURL = originalURL }()

	// Create request
	req := httptest.NewRequest(http.MethodGet, "/api/usage/"+connID+"/glm-reset-cards", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Assertions
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	availableCount, ok := resp["availableCount"].(float64)
	if !ok {
		t.Fatal("availableCount missing or not a number")
	}
	if availableCount != 2 {
		t.Errorf("expected availableCount=2, got %v", availableCount)
	}

	cards, ok := resp["cards"].([]any)
	if !ok {
		t.Fatal("cards missing or not an array")
	}
	if len(cards) != 2 {
		t.Errorf("expected 2 cards, got %d", len(cards))
	}

	// Verify cards are sorted by expiration ascending
	card0 := cards[0].(map[string]any)
	card1 := cards[1].(map[string]any)
	if card0["recordId"] != "card-1" || card1["recordId"] != "card-2" {
		t.Errorf("cards not sorted by expiry: %v, %v", card0, card1)
	}
}

// TestHandleGetGLMResetCards_NotGLM tests that non-GLM connections are rejected.
func TestHandleGetGLMResetCards_NotGLM(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	router := setupTestRouterForGLM(repo)

	// Create a non-GLM connection
	connID := "test-claude-conn-1"
	if err := repo.CreateProviderConnectionFull(connID, "claude", "oauth", "Test Claude", nil, `{"accessToken":"test-claude-token"}`); err != nil {
		t.Fatalf("failed to create connection: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/usage/"+connID+"/glm-reset-cards", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "GLM") {
		t.Errorf("expected error about GLM, got: %s", w.Body.String())
	}
}

// TestHandleGetGLMResetCards_Unauthorized tests handling of 401 from GLM API.
func TestHandleGetGLMResetCards_Unauthorized(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	router := setupTestRouterForGLM(repo)
	connID := "test-glm-conn-2"
	if err := repo.CreateProviderConnectionFull(connID, "glm", "api_key", "Test GLM", nil, `{"apiKey":"test-glm-key"}`); err != nil {
		t.Fatalf("failed to create connection: %v", err)
	}

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer mockServer.Close()

	originalURL := glmResetListURL
	glmResetListURL = mockServer.URL
	defer func() { glmResetListURL = originalURL }()

	req := httptest.NewRequest(http.MethodGet, "/api/usage/"+connID+"/glm-reset-cards", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "expired") {
		t.Errorf("expected token expired message, got: %s", w.Body.String())
	}
}

// TestHandleRedeemGLMResetCard_Validation tests input validation.
func TestHandleRedeemGLMResetCard_Validation(t *testing.T) {
	testCases := []struct {
		name       string
		body       string
		wantStatus int
		wantError  string
	}{
		{
			name:       "missing recordId",
			body:       `{"type":"FIVE_HOUR"}`,
			wantStatus: http.StatusBadRequest,
			wantError:  "recordId is required",
		},
		{
			name:       "missing type",
			body:       `{"recordId":"card-1"}`,
			wantStatus: http.StatusBadRequest,
			wantError:  "type is required",
		},
		{
			name:       "invalid type",
			body:       `{"recordId":"card-1","type":"INVALID"}`,
			wantStatus: http.StatusBadRequest,
			wantError:  "type must be FIVE_HOUR or WEEK",
		},
		{
			name:       "empty body",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantError:  "recordId is required",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			repo, cleanup := setupTestDB(t)
			defer cleanup()

			router := setupTestRouterForGLM(repo)
			connID := "test-glm-conn-3"
			if err := repo.CreateProviderConnectionFull(connID, "glm", "api_key", "Test GLM", nil, `{"apiKey":"test-glm-key"}`); err != nil {
				t.Fatalf("failed to create connection: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/api/usage/"+connID+"/glm-reset-cards", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			router.ServeHTTP(w, req)

			if w.Code != tc.wantStatus {
				t.Errorf("expected status %d, got %d: %s", tc.wantStatus, w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.wantError) {
				t.Errorf("expected error %q, got: %s", tc.wantError, w.Body.String())
			}
		})
	}
}

// TestParseGLMResetExpireTime tests the expire time parsing logic.
func TestParseGLMResetExpireTime(t *testing.T) {
	testCases := []struct {
		name      string
		input     string
		want      string
		wantEmpty bool
	}{
		{
			name:  "ISO8601 with Z",
			input: "2026-12-31T23:59:59Z",
			want:  "2026-12-31T23:59:59Z",
		},
		{
			name:  "ISO8601 with offset",
			input: "2026-12-31T23:59:59+08:00",
			want:  "2026-12-31T15:59:59Z", // Converted to UTC
		},
		{
			name:  "Beijing naive datetime",
			input: "2026-12-31 23:59:59",
			want:  "2026-12-31T15:59:59Z", // Beijing UTC+8 -> UTC
		},
		{
			name:  "Beijing naive date only",
			input: "2026-12-31",
			want:  "2026-12-30T16:00:00Z", // Midnight Beijing = 16:00 UTC previous day
		},
		{
			name:      "empty string",
			input:     "",
			wantEmpty: true,
		},
		{
			name:      "invalid format",
			input:     "not-a-date",
			wantEmpty: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, _ := parseGLMResetExpireTime(tc.input)
			if tc.wantEmpty {
				if result != "" {
					t.Errorf("expected empty, got %q", result)
				}
			} else {
				if result != tc.want {
					t.Errorf("expected %q, got %q", tc.want, result)
				}
			}
		})
	}
}

// TestParseGLMResetCardsResponse tests the response parsing logic.
func TestParseGLMResetCardsResponse(t *testing.T) {
	testCases := []struct {
		name          string
		input         map[string]any
		wantCount     int
		wantFirstID   string
		wantFirstType string
	}{
		{
			name: "standard response with buckets wrapped in data",
			input: wrapZaiResponse(map[string]any{
				"fiveHourResets": []any{
					map[string]any{"recordId": "card-1", "available": true, "expireTime": "2026-12-31 23:59:59"},
				},
				"weekResets": []any{
					map[string]any{"recordId": "card-2", "available": true, "expireTime": "2027-01-15T23:59:59Z"},
				},
			}),
			wantCount:     2,
			wantFirstID:   "card-1",
			wantFirstType: "FIVE_HOUR",
		},
		{
			name: "filters unavailable cards",
			input: wrapZaiResponse(map[string]any{
				"fiveHourResets": []any{
					map[string]any{"recordId": "card-1", "available": true, "expireTime": "2026-12-31 23:59:59"},
					map[string]any{"recordId": "card-2", "available": false, "expireTime": "2026-12-31 23:59:59"},
				},
			}),
			wantCount:   1,
			wantFirstID: "card-1",
		},
		{
			name: "filters cards without recordId",
			input: wrapZaiResponse(map[string]any{
				"fiveHourResets": []any{
					map[string]any{"recordId": "", "available": true, "expireTime": "2026-12-31 23:59:59"},
					map[string]any{"recordId": "card-1", "available": true, "expireTime": "2026-12-31 23:59:59"},
				},
			}),
			wantCount:   1,
			wantFirstID: "card-1",
		},
		{
			name: "filters cards with unparseable expireTime",
			input: wrapZaiResponse(map[string]any{
				"fiveHourResets": []any{
					map[string]any{"recordId": "card-1", "available": true, "expireTime": "invalid"},
					map[string]any{"recordId": "card-2", "available": true, "expireTime": "2026-12-31 23:59:59"},
				},
			}),
			wantCount:   1,
			wantFirstID: "card-2",
		},
		{
			name: "sorts by expiration ascending",
			input: wrapZaiResponse(map[string]any{
				"fiveHourResets": []any{
					map[string]any{"recordId": "card-later", "available": true, "expireTime": "2027-01-15 23:59:59"},
					map[string]any{"recordId": "card-sooner", "available": true, "expireTime": "2026-12-31 23:59:59"},
				},
			}),
			wantCount:   2,
			wantFirstID: "card-sooner",
		},
		{
			name: "accepts numeric recordId",
			input: wrapZaiResponse(map[string]any{
				"fiveHourResets": []any{
					map[string]any{"recordId": 12345, "available": true, "expireTime": "2026-12-31 23:59:59"},
				},
			}),
			wantCount:   1,
			wantFirstID: "12345",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := parseGLMResetCardsResponse(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			availableCount, ok := result["availableCount"].(float64)
			if !ok {
				t.Fatal("availableCount missing")
			}
			if int(availableCount) != tc.wantCount {
				t.Errorf("expected count %d, got %v", tc.wantCount, availableCount)
			}

			cards, ok := result["cards"].([]any)
			if !ok {
				t.Fatal("cards missing")
			}
			if len(cards) != tc.wantCount {
				t.Errorf("expected %d cards, got %d", tc.wantCount, len(cards))
			}
			if tc.wantCount > 0 && tc.wantFirstID != "" {
				firstCard := cards[0].(map[string]any)
				if firstCard["recordId"] != tc.wantFirstID {
					t.Errorf("expected first card ID %q, got %q", tc.wantFirstID, firstCard["recordId"])
				}
				if tc.wantFirstType != "" && firstCard["type"] != tc.wantFirstType {
					t.Errorf("expected first card type %q, got %q", tc.wantFirstType, firstCard["type"])
				}
			}
		})
	}
}

// TestFetchGLMResetCards_Integration tests the full fetch flow with a mock server.
func TestFetchGLMResetCards_Integration(t *testing.T) {
	// Create a test server that mimics the GLM API
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/biz/customer-package-reset/list" && r.URL.Query().Get("targetType") == "PERSONAL" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer test-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			response := wrapZaiResponse(map[string]any{
				"fiveHourResets": []any{
					map[string]any{"recordId": "fh-1", "available": true, "expireTime": "2026-12-31 23:59:59"},
					map[string]any{"recordId": "fh-2", "available": false, "expireTime": "2026-12-31 23:59:59"},
				},
				"weekResets": []any{
					map[string]any{"recordId": "wk-1", "available": true, "expireTime": "2027-01-15T23:59:59Z"},
				},
			})
			json.NewEncoder(w).Encode(response)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	originalURL := glmResetListURL
	glmResetListURL = mockServer.URL + "/api/biz/customer-package-reset/list"
	defer func() { glmResetListURL = originalURL }()

	ctx := context.Background()
	result, err := fetchGLMResetCards(ctx, "test-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("expected result, got nil")
	}

	availableCount, _ := result["availableCount"].(float64)
	if int(availableCount) != 2 {
		t.Errorf("expected 2 available cards, got %v", availableCount)
	}

	cards, _ := result["cards"].([]any)
	if len(cards) != 2 {
		t.Errorf("expected 2 cards in result, got %d", len(cards))
	}
}

// TestWritePlainError ensures writePlainError works (it's used by the handlers).
func TestWritePlainError(t *testing.T) {
	w := httptest.NewRecorder()
	writePlainError(w, http.StatusBadRequest, "test error message")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "test error message") {
		t.Errorf("expected error message in body, got: %s", w.Body.String())
	}
}

// TestUsageJSON tests the usageJSON helper.
func TestUsageJSON(t *testing.T) {
	// Valid JSON
	valid := []byte(`{"key":"value"}`)
	result := usageJSON(valid)
	if result == nil {
		t.Fatal("expected non-nil result for valid JSON")
	}
	if result["key"] != "value" {
		t.Errorf("expected key=value, got %v", result)
	}

	// Invalid JSON
	invalid := []byte(`not json`)
	result = usageJSON(invalid)
	if result != nil {
		t.Errorf("expected nil for invalid JSON, got %v", result)
	}

	// Empty
	result = usageJSON([]byte(""))
	if result != nil {
		t.Errorf("expected nil for empty body, got %v", result)
	}
}

// TestHandlerUtilWriteJSON tests the handlerutil.WriteJSON function.
func TestHandlerUtilWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	data := map[string]any{"status": "ok", "count": 42}
	handlerutil.WriteJSON(w, http.StatusOK, data)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("expected JSON content type, got %s", w.Header().Get("Content-Type"))
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if resp["status"] != "ok" || resp["count"] != float64(42) {
		t.Errorf("unexpected response: %v", resp)
	}
}

// TestParseGLMResetCardsResponse_MissingRecordID tests that missing or null recordId is filtered out.
func TestParseGLMResetCardsResponse_MissingRecordID(t *testing.T) {
	testCases := []struct {
		name     string
		input    map[string]any
		wantZero bool
	}{
		{
			name: "missing recordId key",
			input: wrapZaiResponse(map[string]any{
				"fiveHourResets": []any{
					map[string]any{"available": true, "expireTime": "2026-12-31 23:59:59"},
				},
			}),
			wantZero: true,
		},
		{
			name: "recordId is null",
			input: wrapZaiResponse(map[string]any{
				"fiveHourResets": []any{
					map[string]any{"recordId": nil, "available": true, "expireTime": "2026-12-31 23:59:59"},
				},
			}),
			wantZero: true,
		},
		{
			name: "recordId is empty string",
			input: wrapZaiResponse(map[string]any{
				"fiveHourResets": []any{
					map[string]any{"recordId": "", "available": true, "expireTime": "2026-12-31 23:59:59"},
				},
			}),
			wantZero: true,
		},
		{
			name: "valid recordId passes through",
			input: wrapZaiResponse(map[string]any{
				"fiveHourResets": []any{
					map[string]any{"recordId": "valid-card", "available": true, "expireTime": "2026-12-31 23:59:59"},
				},
			}),
			wantZero: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := parseGLMResetCardsResponse(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			availableCount, ok := result["availableCount"].(float64)
			if !ok {
				t.Fatal("availableCount missing")
			}

			if tc.wantZero {
				if int(availableCount) != 0 {
					t.Errorf("expected 0 cards, got %v", availableCount)
				}
			} else {
				if int(availableCount) != 1 {
					t.Errorf("expected 1 card, got %v", availableCount)
				}
			}
		})
	}
}

// TestParseGLMResetCardsResponse_LargeNumericRecordID tests large numeric recordId formats without scientific notation.
func TestParseGLMResetCardsResponse_LargeNumericRecordID(t *testing.T) {
	// 1735689600000 decoded from JSON yields a float64 (where %g would produce 1.7356896e+12)
	largeID := float64(1735689600000)

	input := wrapZaiResponse(map[string]any{
		"fiveHourResets": []any{
			map[string]any{"recordId": largeID, "available": true, "expireTime": "2026-12-31 23:59:59"},
		},
	})

	result, err := parseGLMResetCardsResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	availableCount, ok := result["availableCount"].(float64)
	if !ok || int(availableCount) != 1 {
		t.Fatalf("expected 1 card, got %v", availableCount)
	}

	cards, ok := result["cards"].([]any)
	if !ok || len(cards) != 1 {
		t.Fatal("cards missing")
	}

	firstCard := cards[0].(map[string]any)
	recordID := firstCard["recordId"]
	if recordID != "1735689600000" {
		t.Errorf("expected recordId '1735689600000' without scientific notation, got %q", recordID)
	}
}

// TestHandleRedeemGLMResetCard_InFlightLock tests that concurrent redemptions return 409.
func TestHandleRedeemGLMResetCard_InFlightLock(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	router := setupTestRouterForGLM(repo)
	connID := "test-glm-conn-inflight"
	if err := repo.CreateProviderConnectionFull(connID, "glm", "api_key", "Test GLM", nil, `{"apiKey":"test-glm-key"}`); err != nil {
		t.Fatalf("failed to create connection: %v", err)
	}

	// Mock server for the list endpoint (returns a valid card)
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/biz/customer-package-reset/list" && r.URL.Query().Get("targetType") == "PERSONAL" {
			w.Header().Set("Content-Type", "application/json")
			response := wrapZaiResponse(map[string]any{
				"fiveHourResets": []any{
					map[string]any{"recordId": "card-1", "available": true, "expireTime": "2027-12-31 23:59:59"},
				},
			})
			json.NewEncoder(w).Encode(response)
		} else if r.URL.Path == "/api/biz/customer-package-reset/use" {
			// Simulate slow response to allow concurrent request to hit
			time.Sleep(200 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(wrapZaiResponse(map[string]any{}))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	originalListURL := glmResetListURL
	originalUseURL := glmResetUseURL
	glmResetListURL = mockServer.URL + "/api/biz/customer-package-reset/list"
	glmResetUseURL = mockServer.URL + "/api/biz/customer-package-reset/use"
	defer func() {
		glmResetListURL = originalListURL
		glmResetUseURL = originalUseURL
	}()

	// First request - should succeed (get the lock)
	body := `{"recordId":"card-1","type":"FIVE_HOUR"}`
	req1 := httptest.NewRequest(http.MethodPost, "/api/usage/"+connID+"/glm-reset-cards", strings.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()

	// Second request - should get 409 conflict
	req2 := httptest.NewRequest(http.MethodPost, "/api/usage/"+connID+"/glm-reset-cards", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		router.ServeHTTP(w1, req1)
	}()

	// Small delay to let first request acquire the in-flight lock
	time.Sleep(10 * time.Millisecond)

	go func() {
		defer wg.Done()
		router.ServeHTTP(w2, req2)
	}()

	wg.Wait()

	// One should succeed (200), one should get 409 conflict
	successCount := 0
	conflictCount := 0
	switch w1.Code {
	case http.StatusOK:
		successCount++
	case http.StatusConflict:
		conflictCount++
	}
	switch w2.Code {
	case http.StatusOK:
		successCount++
	case http.StatusConflict:
		conflictCount++
	}

	if successCount != 1 {
		t.Errorf("expected exactly 1 success, got %d (w1=%d, w2=%d)", successCount, w1.Code, w2.Code)
	}
	if conflictCount != 1 {
		t.Errorf("expected exactly 1 conflict (409), got %d (w1=%d, w2=%d)", conflictCount, w1.Code, w2.Code)
	}
}

// TestHandleRedeemGLMResetCard_ExpiredCard tests that expired card returns 409.
func TestHandleRedeemGLMResetCard_ExpiredCard(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	router := setupTestRouterForGLM(repo)
	connID := "test-glm-conn-expired"
	if err := repo.CreateProviderConnectionFull(connID, "glm", "api_key", "Test GLM", nil, `{"apiKey":"test-glm-key"}`); err != nil {
		t.Fatalf("failed to create connection: %v", err)
	}

	// Mock server for the list endpoint (returns an expired card)
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/biz/customer-package-reset/list" && r.URL.Query().Get("targetType") == "PERSONAL" {
			w.Header().Set("Content-Type", "application/json")
			// Card expired yesterday
			yesterday := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
			response := wrapZaiResponse(map[string]any{
				"fiveHourResets": []any{
					map[string]any{"recordId": "card-expired", "available": true, "expireTime": yesterday},
				},
			})
			json.NewEncoder(w).Encode(response)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	originalURL := glmResetListURL
	glmResetListURL = mockServer.URL + "/api/biz/customer-package-reset/list"
	defer func() { glmResetListURL = originalURL }()

	body := `{"recordId":"card-expired","type":"FIVE_HOUR"}`
	req := httptest.NewRequest(http.MethodPost, "/api/usage/"+connID+"/glm-reset-cards", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected status 409 (conflict) for expired card, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no longer available") && !strings.Contains(w.Body.String(), "expired") {
		t.Errorf("expected error about expired or unavailable card, got: %s", w.Body.String())
	}
}

// TestHandleRedeemGLMResetCard_UncertainFailures tests that wire non-success responses (401, 400, alreadyUsed)
// map to 502 with uncertain: true, avoiding dashboard logout or false positives.
func TestHandleRedeemGLMResetCard_UncertainFailures(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	router := setupTestRouterForGLM(repo)

	connID := "test-glm-conn-uncertain"
	if err := repo.CreateProviderConnectionFull(connID, "glm", "api_key", "Test GLM Uncertain", nil, `{"apiKey":"test-glm-key"}`); err != nil {
		t.Fatalf("failed to create connection: %v", err)
	}

	testCases := []struct {
		name        string
		zaiStatus   int
		zaiPayload  map[string]any
		wantAlready bool
	}{
		{
			name:        "upstream 401 token invalid maps to 502 uncertain",
			zaiStatus:   http.StatusUnauthorized,
			zaiPayload:  map[string]any{"success": false, "code": 401, "message": "token invalid"},
			wantAlready: false,
		},
		{
			name:        "upstream 400 bad request maps to 502 uncertain",
			zaiStatus:   http.StatusBadRequest,
			zaiPayload:  map[string]any{"success": false, "code": 400, "message": "card already used"},
			wantAlready: false,
		},
		{
			name:        "upstream 200 with alreadyUsed: true maps to 502 uncertain",
			zaiStatus:   http.StatusOK,
			zaiPayload:  map[string]any{"success": true, "code": 0, "alreadyUsed": true, "message": "already consumed"},
			wantAlready: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			futureTime := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/biz/customer-package-reset/list":
					w.Header().Set("Content-Type", "application/json")
					response := wrapZaiResponse(map[string]any{
						"fiveHourResets": []any{
							map[string]any{"recordId": "card-valid", "available": true, "expireTime": futureTime},
						},
					})
					_ = json.NewEncoder(w).Encode(response)
				case "/api/biz/customer-package-reset/use":
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.zaiStatus)
					_ = json.NewEncoder(w).Encode(tc.zaiPayload)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer mockServer.Close()

			originalListURL := glmResetListURL
			originalUseURL := glmResetUseURL
			glmResetListURL = mockServer.URL + "/api/biz/customer-package-reset/list"
			glmResetUseURL = mockServer.URL + "/api/biz/customer-package-reset/use"
			defer func() {
				glmResetListURL = originalListURL
				glmResetUseURL = originalUseURL
			}()

			body := `{"recordId":"card-valid","type":"FIVE_HOUR"}`
			req := httptest.NewRequest(http.MethodPost, "/api/usage/"+connID+"/glm-reset-cards", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			router.ServeHTTP(w, req)

			if w.Code != http.StatusBadGateway {
				t.Fatalf("expected status 502, got %d: %s", w.Code, w.Body.String())
			}

			var resp struct {
				Reset       bool   `json:"reset"`
				AlreadyUsed bool   `json:"alreadyUsed"`
				Uncertain   bool   `json:"uncertain"`
				Code        string `json:"code"`
				Message     string `json:"message"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}

			if resp.Reset {
				t.Errorf("expected reset: false, got true")
			}
			if !resp.Uncertain {
				t.Errorf("expected uncertain: true, got false")
			}
			if resp.Code != "uncertain" {
				t.Errorf("expected code: 'uncertain', got %q", resp.Code)
			}
			if resp.AlreadyUsed != tc.wantAlready {
				t.Errorf("expected alreadyUsed: %v, got %v", tc.wantAlready, resp.AlreadyUsed)
			}
		})
	}
}
