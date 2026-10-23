package clientconfig

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenCodeRoundTrip(t *testing.T) {
	home := t.TempDir()
	path := opencodePaths(home).Files[0].Path
	original := `{
  // my providers
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "other": {"npm": "x", "options": {"baseURL": "http://other"}}
  }
}
`
	writeFixture(t, path, original)

	opts := Options{Home: home, BaseURL: "http://localhost:20130", APIKey: "sk-oc-1", Model: "cc/claude-sonnet-5-5"}
	if _, err := newOpenCodeAdapter().Apply(opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := readFixture(t, path)
	for _, want := range []string{
		"// my providers",
		`"$schema"`,
		`"other"`, // the user's provider survives
		`"9router"`,
		`"@ai-sdk/openai-compatible"`,
		`"baseURL": "http://localhost:20130/v1"`,
		`"apiKey": "sk-oc-1"`,
		`"model": "9router/cc/claude-sonnet-5-5"`,
		`"explorer"`,
		`"mode": "subagent"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("after Apply, missing %q\ngot:\n%s", want, got)
		}
	}
	if strings.Contains(got, "9router/9router/") {
		t.Error("the model prefix was applied twice")
	}

	if _, err := newOpenCodeAdapter().Reset(Options{Home: home}); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	back := readFixture(t, path)
	if strings.Contains(back, "9router") {
		t.Errorf("Reset left our keys behind:\n%s", back)
	}
	for _, keep := range []string{"// my providers", `"$schema"`, `"other"`} {
		if !strings.Contains(back, keep) {
			t.Errorf("Reset lost %q:\n%s", keep, back)
		}
	}
}

// Reset must leave a model the user pointed elsewhere, even in the same keys.
func TestOpenCodeResetKeepsForeignKey(t *testing.T) {
	home := t.TempDir()
	path := opencodePaths(home).Files[0].Path
	writeFixture(t, path, `{"model": "anthropic/claude", "agent": {"explorer": {"model": "anthropic/claude"}}}`+"\n")

	if _, err := newOpenCodeAdapter().Reset(Options{Home: home}); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	got := readFixture(t, path)
	if !strings.Contains(got, `"model": "anthropic/claude"`) {
		t.Errorf("Reset removed a model that was not ours:\n%s", got)
	}
}

func TestDroidApplyReindexes(t *testing.T) {
	home := t.TempDir()
	path := droidPaths(home).Files[0].Path
	writeFixture(t, path, `{"customModels": [
  {"id": "custom:mine", "model": "gpt-5", "index": 0, "baseUrl": "http://x"},
  {"id": "custom:other", "model": "o3", "index": 1, "baseUrl": "http://y"}
]}`+"\n")

	opts := Options{Home: home, BaseURL: "http://localhost:20130", APIKey: "sk-d", Model: "cc/m"}
	if _, err := newDroidAdapter().Apply(opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := readFixture(t, path)

	var parsed struct {
		CustomModels []struct {
			ID    string `json:"id"`
			Index int    `json:"index"`
		} `json:"customModels"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, got)
	}
	if len(parsed.CustomModels) != 3 {
		t.Fatalf("got %d entries, want 3 (ours + the user's two)", len(parsed.CustomModels))
	}
	// Ours first, then the user's in their original order.
	if !strings.HasPrefix(parsed.CustomModels[0].ID, droidMarker) {
		t.Errorf("first entry is %q, want ours", parsed.CustomModels[0].ID)
	}
	if parsed.CustomModels[1].ID != "custom:mine" || parsed.CustomModels[2].ID != "custom:other" {
		t.Errorf("user entries lost or reordered: %+v", parsed.CustomModels)
	}
	// The whole array renumbered, which is the point.
	for i, e := range parsed.CustomModels {
		if e.Index != i {
			t.Errorf("entry %d has index %d", i, e.Index)
		}
	}

	if _, err := newDroidAdapter().Reset(Options{Home: home}); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	back := readFixture(t, path)
	if strings.Contains(back, droidMarker) {
		t.Errorf("Reset left our entry behind:\n%s", back)
	}
	if err := json.Unmarshal([]byte(back), &parsed); err != nil {
		t.Fatalf("after Reset, output is not valid JSON: %v", err)
	}
	if len(parsed.CustomModels) != 2 {
		t.Errorf("after Reset, got %d entries, want the user's 2", len(parsed.CustomModels))
	}
	for i, e := range parsed.CustomModels {
		if e.Index != i {
			t.Errorf("after Reset, entry %d has index %d", i, e.Index)
		}
	}
}
