package clientconfig

import (
	"strings"
	"testing"
)

func TestCodexRoundTrip(t *testing.T) {
	home := t.TempDir()
	path := codexPaths(home).Files[0].Path
	original := `# my codex config
model = "gpt-5"
approval_policy = "on-request"

[agents]
max_threads = 4
`
	writeFixture(t, path, original)

	opts := Options{Home: home, BaseURL: "http://localhost:20130", APIKey: "sk-codex-1", Model: "cc/claude-sonnet-5-5"}
	res, err := newCodexAdapter().Apply(opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Changed {
		t.Fatal("Apply reported no change")
	}

	got := readFixture(t, path)
	for _, want := range []string{
		"# my codex config",
		`approval_policy = "on-request"`,
		`max_threads = 4`,
		`model = "cc/claude-sonnet-5-5"`,
		`model_provider = "9router"`,
		`[model_providers.9router]`,
		`name = "9Router"`,
		`base_url = "http://localhost:20130/v1"`,
		`wire_api = "responses"`,
		`Authorization = "Bearer sk-codex-1"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("after Apply, missing %q\ngot:\n%s", want, got)
		}
	}

	// The Authorization header must appear exactly once, no matter how many
	// times Apply runs. Node's writer stacked the table on repeat (§3.2).
	if n := strings.Count(got, "Authorization"); n != 1 {
		t.Errorf("Authorization appears %d times, want 1", n)
	}

	if _, err := newCodexAdapter().Reset(Options{Home: home}); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	back := readFixture(t, path)
	if strings.Contains(back, "9router") {
		t.Errorf("Reset left 9router keys behind:\n%s", back)
	}
	for _, keep := range []string{"# my codex config", `approval_policy = "on-request"`, `max_threads = 4`} {
		if !strings.Contains(back, keep) {
			t.Errorf("Reset lost %q:\n%s", keep, back)
		}
	}
}

// Three Applies must still leave one Authorization line: the second and third
// change nothing, and the table is never appended twice.
func TestCodexApplyIsIdempotent(t *testing.T) {
	home := t.TempDir()
	path := codexPaths(home).Files[0].Path
	opts := Options{Home: home, BaseURL: "http://h:1", APIKey: "k", Model: "m"}

	for i := 0; i < 3; i++ {
		if _, err := newCodexAdapter().Apply(opts); err != nil {
			t.Fatalf("Apply %d: %v", i+1, err)
		}
	}
	got := readFixture(t, path)
	if n := strings.Count(got, `[model_providers.9router]`); n != 1 {
		t.Errorf("table header appears %d times, want 1:\n%s", n, got)
	}
	if n := strings.Count(got, "Authorization"); n != 1 {
		t.Errorf("Authorization appears %d times, want 1:\n%s", n, got)
	}
}

// Reset must not remove a model_provider the user changed by hand.
func TestCodexResetKeepsForeignProvider(t *testing.T) {
	home := t.TempDir()
	path := codexPaths(home).Files[0].Path
	writeFixture(t, path, `model = "gpt-5"
model_provider = "openai"
`)

	if _, err := newCodexAdapter().Reset(Options{Home: home}); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	got := readFixture(t, path)
	for _, keep := range []string{`model = "gpt-5"`, `model_provider = "openai"`} {
		if !strings.Contains(got, keep) {
			t.Errorf("Reset removed %q, which was not ours:\n%s", keep, got)
		}
	}
}

// The legacy agents.subagent key is removed by Apply, because leaving both it
// and default_subagent_model gives Codex two answers.
func TestCodexApplyDropsLegacySubagentKey(t *testing.T) {
	home := t.TempDir()
	path := codexPaths(home).Files[0].Path
	writeFixture(t, path, "[agents]\nsubagent = \"old\"\n")

	if _, err := newCodexAdapter().Apply(Options{Home: home, BaseURL: "http://h:1", APIKey: "k"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readFixture(t, path); strings.Contains(got, "subagent =") {
		t.Errorf("legacy agents.subagent survived Apply:\n%s", got)
	}
}

func TestCodexRefusesMalformedFile(t *testing.T) {
	home := t.TempDir()
	path := codexPaths(home).Files[0].Path
	broken := "model = \n[[["
	writeFixture(t, path, broken)

	if _, err := newCodexAdapter().Apply(Options{Home: home, BaseURL: "http://h:1", APIKey: "k"}); err == nil {
		t.Fatal("Apply accepted a malformed TOML file")
	}
	if got := readFixture(t, path); got != broken {
		t.Errorf("the file changed despite the error:\n%s", got)
	}
}
