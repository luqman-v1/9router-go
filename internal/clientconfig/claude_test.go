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
