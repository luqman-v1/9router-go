package tokensaver

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestCaveman_PromptLanguages(t *testing.T) {
	en := GetCavemanPromptWithLang("full", "en")
	if !strings.Contains(en, "terse caveman") {
		t.Errorf("expected English prompt, got: %s", en)
	}

	id := GetCavemanPromptWithLang("full", "id")
	if !strings.Contains(id, "manusia purba") {
		t.Errorf("expected Indonesian prompt, got: %s", id)
	}
}

func TestCaveman_ShouldBypassCaveman(t *testing.T) {
	destructive := "Please run rm -rf /var/log/* and restart the server"
	if !ShouldBypassCaveman(destructive) {
		t.Error("expected ShouldBypassCaveman = true for rm -rf")
	}

	dropTable := "DROP TABLE users;"
	if !ShouldBypassCaveman(dropTable) {
		t.Error("expected ShouldBypassCaveman = true for DROP TABLE")
	}

	security := "We found a critical CVE-2024-1234 remote code execution vulnerability"
	if !ShouldBypassCaveman(security) {
		t.Error("expected ShouldBypassCaveman = true for security vulnerability")
	}

	detailed := "Can you explain in detail how this hashing algorithm works?"
	if !ShouldBypassCaveman(detailed) {
		t.Error("expected ShouldBypassCaveman = true for 'explain in detail'")
	}

	normal := "Write a function to sort an array of ints"
	if ShouldBypassCaveman(normal) {
		t.Error("expected ShouldBypassCaveman = false for normal request")
	}
}

func TestCaveman_CompressInputPrompt(t *testing.T) {
	input := "Hello, can you please help me to write a quicksort in Go? Thank you!"
	compressed := CompressInputPrompt(input, "")
	if strings.Contains(strings.ToLower(compressed), "hello") {
		t.Errorf("expected greeting stripped: %s", compressed)
	}
	if strings.Contains(strings.ToLower(compressed), "can you please help me to") {
		t.Errorf("expected request lead stripped: %s", compressed)
	}
	if strings.Contains(strings.ToLower(compressed), "thank you") {
		t.Errorf("expected pleasantry tail stripped: %s", compressed)
	}
	if !strings.Contains(compressed, "quicksort in Go") {
		t.Errorf("expected core substance preserved, got: %s", compressed)
	}

	// Preserved keywords test
	preserved := CompressInputPrompt("Please help me debug this error", "please")
	if !strings.Contains(strings.ToLower(preserved), "please") {
		t.Errorf("expected 'please' to be preserved when in preserveKeywords: %s", preserved)
	}
}

func TestCaveman_CompressInputMessages(t *testing.T) {
	req := map[string]any{
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "Hi, please help me to create a Dockerfile. Thanks!",
			},
		},
	}
	body, _ := json.Marshal(req)
	out, changed := CompressInputMessages(body, "")
	if !changed {
		t.Fatal("expected CompressInputMessages to report changed = true")
	}
	var res map[string]any
	json.Unmarshal(out, &res)
	msgs := res["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].(string)
	if strings.Contains(strings.ToLower(content), "hi") || strings.Contains(strings.ToLower(content), "thanks") {
		t.Fatalf("expected filler stripped, got: %s", content)
	}
}
