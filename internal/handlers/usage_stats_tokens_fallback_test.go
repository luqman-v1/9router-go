package handlers

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// TestUsageStats_InputTokensFallback covers upstream 813a0121 parity:
//  1. Rows recorded with input_tokens/output_tokens in the tokens JSON blob (e.g. embeddings, Gemini)
//     are counted in the 24h and today stats when promptTokens/completionTokens columns are 0.
//  2. API key lookup trims whitespace and key-name fallback displays masked key instead of raw prefix.
func TestUsageStats_InputTokensFallback(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	resetUsageWindowCache()
	t.Cleanup(resetUsageWindowCache)

	const cleanKey = "sk-8b71f86e0a1f2fb5-nhz496-cfa1c800"
	const whitespaceKey = "  " + cleanKey + "  "
	const unknownKey = "  sk-unknown-user-machine1-tail9999  "
	const shortKey = "  abcdefgh  "

	if err := repo.CreateApiKey("id-1", cleanKey, "Team Laptop", "machine-1"); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if since := now.Sub(midnight); since < time.Hour {
		t.Skipf("local midnight is only %s ago: row at -1h falls outside today", since)
	}

	// Row 1: promptTokens/completionTokens are 0, tokens JSON carries input_tokens & output_tokens,
	// apiKey has surrounding whitespace.
	tokensBlob1 := `{"input_tokens":150,"output_tokens":75}`
	_, err := repo.RawDB().Exec(
		`INSERT INTO usageHistory (timestamp, provider, model, connectionId, apiKey, endpoint, promptTokens, completionTokens, cost, status, tokens)
		 VALUES (?, ?, ?, ?, ?, '/v1/chat/completions', 0, 0, 0.05, 'success', ?)`,
		now.Add(-1*time.Hour).UTC().Format(time.RFC3339), "gemini", "gemini-2.5-flash", "conn-1", whitespaceKey, tokensBlob1,
	)
	if err != nil {
		t.Fatalf("insert usageHistory row 1: %v", err)
	}

	// Row 2: unknown key with surrounding whitespace, also input_tokens fallback.
	tokensBlob2 := `{"input_tokens":50,"output_tokens":25}`
	_, err = repo.RawDB().Exec(
		`INSERT INTO usageHistory (timestamp, provider, model, connectionId, apiKey, endpoint, promptTokens, completionTokens, cost, status, tokens)
		 VALUES (?, ?, ?, ?, ?, '/v1/embeddings', 0, 0, 0.01, 'success', ?)`,
		now.Add(-30*time.Minute).UTC().Format(time.RFC3339), "openai", "text-embedding-3-small", "conn-2", unknownKey, tokensBlob2,
	)
	if err != nil {
		t.Fatalf("insert usageHistory row 2: %v", err)
	}

	// Row 3: short key with surrounding whitespace; tests upstream charAt(0)+"***" masking
	// and verifies apiKeyKey does not leak the raw key.
	tokensBlob3 := `{"prompt_tokens":10,"completion_tokens":5}`
	_, err = repo.RawDB().Exec(
		`INSERT INTO usageHistory (timestamp, provider, model, connectionId, apiKey, endpoint, promptTokens, completionTokens, cost, status, tokens)
		 VALUES (?, ?, ?, ?, ?, '/v1/chat/completions', 10, 5, 0.01, 'success', ?)`,
		now.Add(-15*time.Minute).UTC().Format(time.RFC3339), "openai", "gpt-4o", "conn-3", shortKey, tokensBlob3,
	)
	if err != nil {
		t.Fatalf("insert usageHistory row 3: %v", err)
	}

	for _, period := range []string{"24h", "today"} {
		t.Run("period="+period, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/usage/stats?period="+period, nil)
			HandleUsageStats(repo)(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
			}

			var resp UsageStatsResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			if resp.TotalPromptTokens != 210 {
				t.Errorf("TotalPromptTokens = %d, want 210 (150+50+10)", resp.TotalPromptTokens)
			}
			if resp.TotalCompletionTokens != 105 {
				t.Errorf("TotalCompletionTokens = %d, want 105 (75+25+5)", resp.TotalCompletionTokens)
			}

			// Check ByProvider
			geminiProv := resp.ByProvider["gemini"]
			if geminiProv.PromptTokens != 150 || geminiProv.CompletionTokens != 75 {
				t.Errorf("gemini provider tokens = prompt %d / compl %d, want 150 / 75", geminiProv.PromptTokens, geminiProv.CompletionTokens)
			}

			// Check ByApiKey: whitespaceKey should resolve to "Team Laptop"
			foundNamedKey := false
			foundUnknownKey := false
			foundShortKey := false
			expectedUnknownMask := handlerutil.MaskAPIKey(unknownKey)

			for _, item := range resp.ByApiKey {
				// Verify apiKeyKey is masked and never leaks raw key
				if item.ApiKeyKey != item.ApiKeyMasked {
					t.Errorf("ApiKeyKey = %q, want ApiKeyMasked %q", item.ApiKeyKey, item.ApiKeyMasked)
				}
				if item.ApiKeyKey == cleanKey || item.ApiKeyKey == unknownKey || item.ApiKeyKey == shortKey {
					t.Errorf("ApiKeyKey %q leaked raw key", item.ApiKeyKey)
				}

				if item.KeyName == "Team Laptop" {
					foundNamedKey = true
					if item.PromptTokens != 150 || item.CompletionTokens != 75 {
						t.Errorf("Team Laptop tokens = prompt %d / compl %d, want 150 / 75", item.PromptTokens, item.CompletionTokens)
					}
				}
				if item.KeyName == expectedUnknownMask {
					foundUnknownKey = true
					if item.PromptTokens != 50 || item.CompletionTokens != 25 {
						t.Errorf("Unknown key tokens = prompt %d / compl %d, want 50 / 25", item.PromptTokens, item.CompletionTokens)
					}
				}
				if item.KeyName == "a***" {
					foundShortKey = true
					if item.ApiKeyMasked != "a***" {
						t.Errorf("Short key ApiKeyMasked = %q, want 'a***'", item.ApiKeyMasked)
					}
					if item.PromptTokens != 10 || item.CompletionTokens != 5 {
						t.Errorf("Short key tokens = prompt %d / compl %d, want 10 / 5", item.PromptTokens, item.CompletionTokens)
					}
				}
			}

			if !foundNamedKey {
				t.Errorf("expected ByApiKey bucket with KeyName 'Team Laptop', got: %+v", resp.ByApiKey)
			}
			if !foundUnknownKey {
				t.Errorf("expected ByApiKey bucket with KeyName %q (masked key fallback), got: %+v", expectedUnknownMask, resp.ByApiKey)
			}
			if !foundShortKey {
				t.Errorf("expected ByApiKey bucket with KeyName 'a***' for short key, got: %+v", resp.ByApiKey)
			}
		})
	}
}
