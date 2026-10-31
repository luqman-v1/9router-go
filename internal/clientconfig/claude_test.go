package clientconfig

import (
	"os"
	"strings"
	"testing"
)

// S1 for claude: Apply then Reset must return the file to its original bytes,
// with hasCompletedOnboarding the single documented exception
// (client-schema-matrix §8.4).
func TestClaudeRoundTrip(t *testing.T) {
	home := t.TempDir()
	paths := claudePaths(home)
	original := `{
  // my own note
  "theme": "dark",
  "env": {
    "ANTHROPIC_MODEL": "keep-me",
    "HTTP_PROXY": "http://proxy.local:8080"
  }
}
`
	writeFixture(t, paths.Files[0].Path, original)

	opts := Options{Home: home, BaseURL: "http://localhost:20130", APIKey: "sk-test-123"}
	res, err := newClaudeAdapter().Apply(opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Changed {
		t.Fatal("Apply reported no change on a fresh file")
	}
	if len(res.Backups) != 1 {
		t.Fatalf("got %d backups, want 1", len(res.Backups))
	}

	after := readFixture(t, paths.Files[0].Path)
	for _, want := range []string{
		"// my own note",               // comment preserved
		`"theme": "dark"`,              // unrelated key preserved
		`"ANTHROPIC_MODEL": "keep-me"`, // sibling env key preserved
		`"HTTP_PROXY": "http://proxy.local:8080"`,
		`"ANTHROPIC_BASE_URL": "http://localhost:20130/v1"`,
		`"ANTHROPIC_AUTH_TOKEN": "sk-test-123"`,
		`"hasCompletedOnboarding": true`, // a boolean, not a string
	} {
		if !strings.Contains(after, want) {
			t.Errorf("after Apply, missing %q\ngot:\n%s", want, after)
		}
	}
	if strings.Contains(after, `"hasCompletedOnboarding": "true"`) {
		t.Error("hasCompletedOnboarding was written as a string")
	}

	if _, err := newClaudeAdapter().Reset(Options{Home: home}); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	back := readFixture(t, paths.Files[0].Path)

	// Everything Apply added is gone; everything else is untouched.
	for _, gone := range claudeEnvKeys {
		if strings.Contains(back, gone) {
			t.Errorf("after Reset, %s is still present", gone)
		}
	}
	for _, keep := range []string{"// my own note", `"theme": "dark"`, `"ANTHROPIC_MODEL": "keep-me"`, `"HTTP_PROXY": "http://proxy.local:8080"`} {
		if !strings.Contains(back, keep) {
			t.Errorf("after Reset, lost %q\ngot:\n%s", keep, back)
		}
	}
	// The documented exception.
	if !strings.Contains(back, `"hasCompletedOnboarding": true`) {
		t.Error("Reset removed hasCompletedOnboarding, which it must not")
	}
}

// The tier rule: flag beats server, server beats nothing, and nothing means the
// existing key survives untouched (connect-cli-spec §3.2.4).
func TestClaudeTierPrecedence(t *testing.T) {
	tests := []struct {
		name        string
		opts        Options
		wantPresent map[string]string
		wantAbsent  []string
	}{
		{
			name:        "flag wins over server",
			opts:        Options{Tiers: map[string]string{"opus": "from-flag"}, ServerTiers: map[string]string{"opus": "from-server"}},
			wantPresent: map[string]string{"ANTHROPIC_DEFAULT_OPUS_MODEL": "from-flag"},
		},
		{
			name:        "server used when the flag is absent",
			opts:        Options{ServerTiers: map[string]string{"sonnet": "from-server"}},
			wantPresent: map[string]string{"ANTHROPIC_DEFAULT_SONNET_MODEL": "from-server"},
		},
		{
			name:       "neither source leaves the key alone",
			opts:       Options{ServerTiers: map[string]string{"haiku": "   "}},
			wantAbsent: []string{"ANTHROPIC_DEFAULT_HAIKU_MODEL"},
		},
		{
			name: "all four tiers are independent",
			opts: Options{Tiers: map[string]string{
				"fable": "f", "opus": "o", "sonnet": "s", "haiku": "h",
			}},
			wantPresent: map[string]string{
				"ANTHROPIC_DEFAULT_FABLE_MODEL":  "f",
				"ANTHROPIC_DEFAULT_OPUS_MODEL":   "o",
				"ANTHROPIC_DEFAULT_SONNET_MODEL": "s",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "h",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			path := claudePaths(home).Files[0].Path
			// A pre-existing tier key the user set themselves.
			writeFixture(t, path, `{"env": {"ANTHROPIC_DEFAULT_HAIKU_MODEL": "user-set"}}`+"\n")

			tt.opts.Home = home
			tt.opts.BaseURL = "http://h:1"
			tt.opts.APIKey = "k"
			if _, err := newClaudeAdapter().Apply(tt.opts); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			got := readFixture(t, path)
			for k, v := range tt.wantPresent {
				if !strings.Contains(got, `"`+k+`": "`+v+`"`) {
					t.Errorf("missing %s=%s\ngot:\n%s", k, v, got)
				}
			}
			for _, k := range tt.wantAbsent {
				if strings.Contains(got, `"`+k+`"`) {
					// The user's own value must still be there.
					if !strings.Contains(got, `"`+k+`": "user-set"`) {
						t.Errorf("%s was overwritten or removed\ngot:\n%s", k, got)
					}
				}
			}
		})
	}
}

// The second Apply of the same options must change nothing. A connect that
// rewrites an already-correct file would make every run take a backup and
// touch the mtime for no reason.
func TestClaudeApplyIsIdempotent(t *testing.T) {
	home := t.TempDir()
	opts := Options{Home: home, BaseURL: "http://h:1", APIKey: "k"}
	path := claudePaths(home).Files[0].Path

	first, err := newClaudeAdapter().Apply(opts)
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if !first.Changed {
		t.Fatal("first Apply reported no change")
	}
	before := readFixture(t, path)

	second, err := newClaudeAdapter().Apply(opts)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if second.Changed {
		t.Error("second Apply reported a change")
	}
	if len(second.Backups) != 0 {
		t.Errorf("second Apply made %d backups, want 0", len(second.Backups))
	}
	if after := readFixture(t, path); after != before {
		t.Errorf("second Apply changed the file:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// A malformed settings.json must fail closed: an error, no backup, no write.
func TestClaudeRefusesMalformedFile(t *testing.T) {
	home := t.TempDir()
	path := claudePaths(home).Files[0].Path
	broken := `{"env": {"A": `
	writeFixture(t, path, broken)

	_, err := newClaudeAdapter().Apply(Options{Home: home, BaseURL: "http://h:1", APIKey: "k"})
	if err == nil {
		t.Fatal("Apply accepted a malformed file")
	}
	if got := readFixture(t, path); got != broken {
		t.Errorf("the file was modified despite the error:\n%s", got)
	}
}

func readFixture(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestClaudeCurrent(t *testing.T) {
	home := t.TempDir()
	paths := claudePaths(home)
	content := `{
	  "autoCompactWindow": 330000,
	  "model": "opus",
	  "env": {
	    "ANTHROPIC_BASE_URL": "http://localhost:20130/v1",
	    "ANTHROPIC_AUTH_TOKEN": "sk-test",
	    "ANTHROPIC_DEFAULT_SONNET_MODEL": "claude-sonnet-5-5[1m]",
	    "ANTHROPIC_DEFAULT_OPUS_MODEL": "claude-opus-5-5[1m]",
	    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "claude-haiku-5-5"
	  }
	}`
	writeFixture(t, paths.Files[0].Path, content)

	adapter := newClaudeAdapter()
	reader, ok := adapter.(CurrentConfigReader)
	if !ok {
		t.Fatal("claudeAdapter does not implement CurrentConfigReader")
	}
	curr, err := reader.Current(Options{Home: home})
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if curr["baseUrl"] != "http://localhost:20130/v1" {
		t.Errorf("baseUrl = %v, want http://localhost:20130/v1", curr["baseUrl"])
	}
	if curr["model"] != "opus" {
		t.Errorf("model = %v, want opus", curr["model"])
	}
	if curr["autoCompactWindow"] != 330000 {
		t.Errorf("autoCompactWindow = %v (%T), want 330000 (int)", curr["autoCompactWindow"], curr["autoCompactWindow"])
	}
	tiers, ok := curr["tiers"].(map[string]string)
	if !ok {
		t.Fatalf("tiers = %v, want map", curr["tiers"])
	}
	if tiers["sonnet"] != "claude-sonnet-5-5[1m]" {
		t.Errorf("sonnet = %v, want claude-sonnet-5-5[1m]", tiers["sonnet"])
	}
}

func TestClaudeReset_RemovesModelAndAutoCompactWindow(t *testing.T) {
	home := t.TempDir()
	paths := claudePaths(home)
	opts := Options{
		Home:              home,
		BaseURL:           "http://localhost:20130",
		APIKey:            "sk-test",
		Model:             "claude-opus-5-5",
		AutoCompactWindow: "160000",
	}
	adapter := newClaudeAdapter()
	if _, err := adapter.Apply(opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	afterApply := readFixture(t, paths.Files[0].Path)
	if !strings.Contains(afterApply, `"model": "claude-opus-5-5"`) {
		t.Fatalf("model not written:\n%s", afterApply)
	}
	if !strings.Contains(afterApply, `"autoCompactWindow": 160000`) {
		t.Fatalf("autoCompactWindow not written:\n%s", afterApply)
	}

	if _, err := adapter.Reset(Options{Home: home}); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	afterReset := readFixture(t, paths.Files[0].Path)
	if strings.Contains(afterReset, "claude-opus-5-5") || strings.Contains(afterReset, `"model"`) {
		t.Errorf("model was not removed on reset:\n%s", afterReset)
	}
	if strings.Contains(afterReset, "autoCompactWindow") {
		t.Errorf("autoCompactWindow was not removed on reset:\n%s", afterReset)
	}
	// hasCompletedOnboarding must survive as the single documented exception
	if !strings.Contains(afterReset, `"hasCompletedOnboarding": true`) {
		t.Errorf("hasCompletedOnboarding must survive reset:\n%s", afterReset)
	}
}

func TestClaudeReset_PreservesCustomModelWhenNotPointingAtUs(t *testing.T) {
	home := t.TempDir()
	paths := claudePaths(home)
	content := `{
		"model": "user-custom-model",
		"theme": "dark"
	}`
	writeFixture(t, paths.Files[0].Path, content)

	adapter := newClaudeAdapter()
	if _, err := adapter.Reset(Options{Home: home}); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	afterReset := readFixture(t, paths.Files[0].Path)
	if !strings.Contains(afterReset, `"model": "user-custom-model"`) {
		t.Errorf("user's own model was wrongly deleted:\n%s", afterReset)
	}
}

func TestClaudeReset_PreservesCustomModelWithForeignBaseURL(t *testing.T) {
	for _, foreignURL := range []string{
		"https://api.anthropic.com",
		"https://api.anthropic.com/v1",
		"http://localhost:8080/v1",
		"https://my-other-proxy.com",
	} {
		t.Run(foreignURL, func(t *testing.T) {
			home := t.TempDir()
			paths := claudePaths(home)
			content := `{
				"model": "user-custom-model",
				"env": {
					"ANTHROPIC_BASE_URL": "` + foreignURL + `"
				}
			}`
			writeFixture(t, paths.Files[0].Path, content)

			adapter := newClaudeAdapter()
			if _, err := adapter.Reset(Options{Home: home, BaseURL: "http://127.0.0.1:20130"}); err != nil {
				t.Fatalf("Reset: %v", err)
			}
			afterReset := readFixture(t, paths.Files[0].Path)
			if !strings.Contains(afterReset, `"model": "user-custom-model"`) {
				t.Errorf("user's own model was wrongly deleted with foreign URL %s:\n%s", foreignURL, afterReset)
			}
		})
	}
}

func TestClaudeReset_RemovesModelWhenPointingAtGateway(t *testing.T) {
	for _, gatewayURL := range []string{
		"http://127.0.0.1:20130/v1",
		"http://127.0.0.1:20130",
		"http://localhost:20130/v1",
	} {
		t.Run(gatewayURL, func(t *testing.T) {
			home := t.TempDir()
			paths := claudePaths(home)
			content := `{
				"model": "gateway-model",
				"env": {
					"ANTHROPIC_BASE_URL": "` + gatewayURL + `"
				}
			}`
			writeFixture(t, paths.Files[0].Path, content)

			adapter := newClaudeAdapter()
			if _, err := adapter.Reset(Options{Home: home, BaseURL: "http://127.0.0.1:20130"}); err != nil {
				t.Fatalf("Reset: %v", err)
			}
			afterReset := readFixture(t, paths.Files[0].Path)
			if strings.Contains(afterReset, `"model"`) {
				t.Errorf("gateway model should be removed on reset with gateway URL %s:\n%s", gatewayURL, afterReset)
			}
		})
	}
}

func TestClaudeCurrent_DoesNotLeakApiKey(t *testing.T) {
	home := t.TempDir()
	paths := claudePaths(home)
	content := `{
		"env": {
			"ANTHROPIC_BASE_URL": "http://127.0.0.1:20130/v1",
			"ANTHROPIC_AUTH_TOKEN": "sk-ant-secret-123456"
		}
	}`
	writeFixture(t, paths.Files[0].Path, content)

	adapter := newClaudeAdapter()
	curr, err := adapter.(CurrentConfigReader).Current(Options{Home: home})
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if curr["hasApiKey"] != true {
		t.Errorf("expected hasApiKey = true, got %v", curr["hasApiKey"])
	}
	if _, ok := curr["apiKey"]; ok {
		t.Errorf("apiKey must not be present in Current response, got %v", curr["apiKey"])
	}
}

func TestClaudeApply_ClearingModelDeletesWhenPointingAtGateway(t *testing.T) {
	home := t.TempDir()
	paths := claudePaths(home)
	content := `{
		"model": "previous-custom-model",
		"autoCompactWindow": 200000,
		"env": {
			"ANTHROPIC_BASE_URL": "http://127.0.0.1:20130/v1"
		}
	}`
	writeFixture(t, paths.Files[0].Path, content)

	adapter := newClaudeAdapter()
	// Apply with empty Model and empty AutoCompactWindow
	opts := Options{Home: home, BaseURL: "http://127.0.0.1:20130", Model: "", AutoCompactWindow: ""}
	if _, err := adapter.Apply(opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	afterApply := readFixture(t, paths.Files[0].Path)
	if strings.Contains(afterApply, `"model"`) {
		t.Errorf("model key should be removed when cleared while pointing at gateway:\n%s", afterApply)
	}
	if strings.Contains(afterApply, "autoCompactWindow") {
		t.Errorf("autoCompactWindow should be removed when empty:\n%s", afterApply)
	}
}

func TestClaudeApply_ClearingModelPreservesForeignModel(t *testing.T) {
	home := t.TempDir()
	paths := claudePaths(home)
	content := `{
		"model": "user-custom-model",
		"env": {
			"ANTHROPIC_BASE_URL": "https://api.anthropic.com/v1"
		}
	}`
	writeFixture(t, paths.Files[0].Path, content)

	adapter := newClaudeAdapter()
	opts := Options{Home: home, BaseURL: "http://127.0.0.1:20130", Model: ""}
	if _, err := adapter.Apply(opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	afterApply := readFixture(t, paths.Files[0].Path)
	// Model should be preserved because prior config pointed to foreign endpoint
	if !strings.Contains(afterApply, `"model": "user-custom-model"`) {
		t.Errorf("user custom model should be preserved when prior config was foreign:\n%s", afterApply)
	}
}

func TestClaudeReset_DecisiveKnownBaseURL(t *testing.T) {
	home := t.TempDir()
	paths := claudePaths(home)
	// Foreign URL that happens to contain "20130" in the path or domain
	content := `{
		"model": "keep-this-foreign-model",
		"env": {
			"ANTHROPIC_BASE_URL": "http://foreign-site.com:20130/v1"
		}
	}`
	writeFixture(t, paths.Files[0].Path, content)

	adapter := newClaudeAdapter()
	// Known gateway base URL is 20999 (custom port)
	if _, err := adapter.Reset(Options{Home: home, BaseURL: "http://127.0.0.1:20999"}); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	afterReset := readFixture(t, paths.Files[0].Path)
	if !strings.Contains(afterReset, `"model": "keep-this-foreign-model"`) {
		t.Errorf("foreign model was wrongly deleted because substring fallback fired despite known BaseURL:\n%s", afterReset)
	}
}
