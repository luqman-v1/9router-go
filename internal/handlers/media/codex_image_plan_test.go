package media

import (
	"encoding/base64"
	"testing"

	"9router/proxy/internal/handlers/chat"
)

// Upstream b08f12d5: the codex image lane resolves the ChatGPT plan at request
// time from token claims, then the stored providerSpecificData, and rejects
// "free" while allowing unknown plans.

func codexTestToken(plan string) string {
	payload := `{"https://api.openai.com/auth":{"chatgpt_plan_type":"` + plan + `"}}`
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
}

func TestCodexPlanTypeFromTokenClaims(t *testing.T) {
	connData := &chat.ConnectionData{
		APIKey: codexTestToken("free"),
		ProviderSpecificData: map[string]any{
			"chatgptPlanType": "plus", // stored value is stale once the token carries a plan
		},
	}
	if got := codexPlanType(connData); got != "free" {
		t.Errorf("codexPlanType = %q, want the token claim free", got)
	}
}

func TestCodexPlanTypeFallsBackToStoredValue(t *testing.T) {
	connData := &chat.ConnectionData{
		APIKey: "sk-not-a-jwt",
		ProviderSpecificData: map[string]any{
			"chatgptPlanType": "pro",
		},
	}
	if got := codexPlanType(connData); got != "pro" {
		t.Errorf("codexPlanType = %q, want the stored pro", got)
	}
}

func TestCodexPlanTypeUnknownStaysUnknown(t *testing.T) {
	if got := codexPlanType(&chat.ConnectionData{APIKey: "sk-not-a-jwt"}); got != "" {
		t.Errorf("codexPlanType = %q, want empty for claim-less accounts", got)
	}
	connData := &chat.ConnectionData{
		APIKey: "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"email":"x@example.com"}`)) + ".sig",
	}
	if got := codexPlanType(connData); got != "" {
		t.Errorf("codexPlanType = %q, want empty when the auth block carries no plan", got)
	}
}

func TestDecodeOpenAIAuthClaimsGarbage(t *testing.T) {
	for _, token := range []string{"", "not-a-jwt", "a.b", "h.####.s"} {
		if got := decodeOpenAIAuthClaims(token); len(got) != 0 {
			t.Errorf("decodeOpenAIAuthClaims(%q) = %v, want empty", token, got)
		}
	}
}
