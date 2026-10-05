package chat

import (
	json "encoding/json/v2"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/translator"
)

// TestUsageHistoryStoresReasoningTokens covers the stored token JSON: the
// usageHistory row is what the dashboard bills and charts from, so a reasoning
// turn whose reasoning_tokens were missing there read as far cheaper than it
// actually was — even though the same count was already billed and written
// into requestDetails.
func TestUsageHistoryStoresReasoningTokens(t *testing.T) {
	tests := []struct {
		name       string
		usage      translator.OpenAIUsage
		wantReason int
		wantCache  int
	}{
		{
			name: "reasoning and cache reported",
			usage: translator.OpenAIUsage{
				PromptTokens:            25421,
				CompletionTokens:        120,
				CompletionTokensDetails: &translator.CompletionTokensDetails{ReasoningTokens: 65},
			},
			wantReason: 65,
		},
		{
			name: "reasoning only",
			usage: translator.OpenAIUsage{
				PromptTokens:            100,
				CompletionTokens:        40,
				CompletionTokensDetails: &translator.CompletionTokensDetails{ReasoningTokens: 32},
			},
			wantReason: 32,
		},
		{
			name: "cache read still stored",
			usage: translator.OpenAIUsage{
				PromptTokens:             900,
				CompletionTokens:         10,
				CachedTokens:             800,
				CacheCreationInputTokens: 50,
			},
			wantCache: 800,
		},
		{
			name: "no reasoning reported stores zero",
			usage: translator.OpenAIUsage{
				PromptTokens:     80,
				CompletionTokens: 12,
			},
			wantReason: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, cleanup := setupChatTestDB(t)
			defer cleanup()
			h := NewChatHandler(db.NewRepo(database))

			usage := tt.usage
			info := &UsageLogInfo{
				Provider:     "test-provider",
				Model:        "test-model",
				ConnectionID: "conn-reasoning-tokens",
			}
			h.LogUsage(info, &usage, 120, []byte(`{"messages":[{"role":"user","content":"test"}]}`), nil)

			var tokensJSON string
			err := database.QueryRow(`
				SELECT tokens FROM usageHistory
				WHERE connectionId = 'conn-reasoning-tokens'
				ORDER BY rowid DESC LIMIT 1
			`).Scan(&tokensJSON)
			if err != nil {
				t.Fatalf("query usageHistory: %v", err)
			}

			var stored map[string]any
			if err := json.Unmarshal([]byte(tokensJSON), &stored); err != nil {
				t.Fatalf("unmarshal stored tokens: %v (got %q)", err, tokensJSON)
			}

			// The key must be present even at zero: an absent key reads as
			// unknown, which the dashboard treats differently from a real zero.
			if _, ok := stored["reasoning_tokens"]; !ok {
				t.Errorf("stored tokens missing reasoning_tokens: %s", tokensJSON)
			}
			assertStoredInt(t, stored, "reasoning_tokens", tt.wantReason)
			assertStoredInt(t, stored, "cached_tokens", tt.wantCache)
			assertStoredInt(t, stored, "prompt_tokens", usage.PromptTokens)
			assertStoredInt(t, stored, "completion_tokens", usage.CompletionTokens)
		})
	}
}

func assertStoredInt(t *testing.T, stored map[string]any, key string, want int) {
	t.Helper()
	got, ok := stored[key].(float64)
	if !ok {
		t.Errorf("stored[%q] is not a number: %v", key, stored[key])
		return
	}
	if int(got) != want {
		t.Errorf("stored[%q] = %d, want %d", key, int(got), want)
	}
}