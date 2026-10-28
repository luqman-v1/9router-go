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

// The dashboard offers three 文言文 levels next to the English ones. Each has
// to answer with its own doctrine: falling through to CavemanFull handed a
// user who picked "文 Full" an English caveman prompt, and nothing in the
// response said the setting had not applied.
func TestCaveman_WenyanLevelsAreTheirOwnPrompt(t *testing.T) {
	cases := map[string]string{
		"wenyan-lite":  "semi-classical",
		"wenyan":       "文言文",
		"wenyan-ultra": "文言文 ultra",
	}
	for level, want := range cases {
		got := GetCavemanPrompt(level)
		if !strings.Contains(got, want) {
			t.Errorf("GetCavemanPrompt(%q) does not contain %q: %.120s", level, want, got)
		}
		if got == CavemanFull {
			t.Errorf("GetCavemanPrompt(%q) returned the English full prompt", level)
		}
	}
}

// Every level keeps the shared doctrine, so the format rules and the clarity
// override do not silently disappear from a level that was added later.
func TestCaveman_EveryLevelCarriesTheSharedTail(t *testing.T) {
	for _, level := range []string{"lite", "full", "ultra", "wenyan-lite", "wenyan", "wenyan-ultra"} {
		got := GetCavemanPrompt(level)
		if !strings.Contains(got, "Auto-Clarity") {
			t.Errorf("level %q lost the Auto-Clarity clause", level)
		}
		if !strings.Contains(got, "keep exact") {
			t.Errorf("level %q lost the verbatim-payload clause", level)
		}
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
