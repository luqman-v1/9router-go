package clientconfig

import (
	"strings"
	"testing"
)

// itoa lives in backup_test.go and is reused here.

// The connect flow resolves each model's published capacity and hands it over
// in Options.ModelLimits; these tests pin what every adapter does with it —
// including that an unknown model leaves each config exactly as before.

func TestCodexWritesLimitKeysAsIntegers(t *testing.T) {
	home := t.TempDir()
	path := codexPaths(home).Files[0].Path
	opts := Options{
		Home: home, BaseURL: "http://h:1", APIKey: "k", Model: "glm/glm-5.3",
		ModelLimits: map[string]ModelLimits{
			"glm/glm-5.3": {ContextWindow: 1000000, MaxOutput: 128000},
		},
	}
	if _, err := newCodexAdapter().Apply(opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := readFixture(t, path)
	// Integers, not strings: codex reads these as numbers, and a quoted value
	// is a type error at its config load.
	for _, want := range []string{
		`model_context_window = 1000000`,
		`model_auto_compact_token_limit = 900000`,
		`model_max_output_tokens = 128000`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("after Apply, missing %q\ngot:\n%s", want, got)
		}
	}
	if strings.Contains(got, `model_context_window = "1000000"`) {
		t.Errorf("window was written quoted:\n%s", got)
	}

	// Reset removes the limit keys even though they sit outside the provider
	// table: they are global, and one left behind would describe this
	// gateway's window to the next provider the user configures.
	if _, err := newCodexAdapter().Reset(Options{Home: home}); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	back := readFixture(t, path)
	for _, gone := range []string{"model_context_window", "model_auto_compact_token_limit", "model_max_output_tokens"} {
		if strings.Contains(back, gone) {
			t.Errorf("Reset left %q behind:\n%s", gone, back)
		}
	}
}

func TestCodexWithoutLimitsWritesNoLimitKeys(t *testing.T) {
	home := t.TempDir()
	path := codexPaths(home).Files[0].Path
	if _, err := newCodexAdapter().Apply(Options{Home: home, BaseURL: "http://h:1", APIKey: "k", Model: "m"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := readFixture(t, path)
	for _, gone := range []string{"model_context_window", "model_auto_compact_token_limit", "model_max_output_tokens"} {
		if strings.Contains(got, gone) {
			t.Errorf("no limits resolved, yet %q was written:\n%s", gone, got)
		}
	}
}

// A window the user's config already carries (from a previous connect) is a
// scalar replace, not an append — the integer must stay an integer there too.
func TestCodexReplacesExistingLimitKey(t *testing.T) {
	home := t.TempDir()
	path := codexPaths(home).Files[0].Path
	writeFixture(t, path, "model_context_window = 5\nmodel_auto_compact_token_limit = 4\n")
	opts := Options{
		Home: home, BaseURL: "http://h:1", APIKey: "k", Model: "m",
		ModelLimits: map[string]ModelLimits{"m": {ContextWindow: 400000}},
	}
	if _, err := newCodexAdapter().Apply(opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := readFixture(t, path)
	for _, want := range []string{"model_context_window = 400000", "model_auto_compact_token_limit = 360000"} {
		if !strings.Contains(got, want) {
			t.Errorf("after Apply, missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestClaudeAutoCompactWindow(t *testing.T) {
	home := t.TempDir()
	path := claudePaths(home).Files[0].Path
	opts := Options{
		Home: home, BaseURL: "http://h:1", APIKey: "k",
		Model: "glm/big-model",
		Tiers: map[string]string{"sonnet": "glm/small-model"},
		ModelLimits: map[string]ModelLimits{
			"glm/big-model":   {ContextWindow: 1000000},
			"glm/small-model": {ContextWindow: 200000},
		},
	}
	if _, err := newClaudeAdapter().Apply(opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// The env var applies to every model, so the narrowest resolved window is
	// the only value that never overshoots a real limit.
	if got := readFixture(t, path); !strings.Contains(got, `"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "200000"`) {
		t.Errorf("expected the narrowest tier window 200000, got:\n%s", got)
	}
}

func TestClaudeAutoCompactWindowClampsToCLIBounds(t *testing.T) {
	tests := []struct {
		name   string
		window int
		want   int
	}{
		{"above the 1M ceiling", 2000000, 1000000},
		{"below the 100k floor", 64000, 100000},
		{"inside the bounds", 262000, 262000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			path := claudePaths(home).Files[0].Path
			opts := Options{
				Home: home, BaseURL: "http://h:1", APIKey: "k", Model: "m",
				ModelLimits: map[string]ModelLimits{"m": {ContextWindow: tc.window}},
			}
			if _, err := newClaudeAdapter().Apply(opts); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if got := readFixture(t, path); !strings.Contains(got, `"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "`+itoa(tc.want)+`"`) {
				t.Errorf("expected window %d, got:\n%s", tc.want, got)
			}
		})
	}
}

func TestClaudeWithoutLimitsWritesNoAutoCompactWindow(t *testing.T) {
	home := t.TempDir()
	path := claudePaths(home).Files[0].Path
	if _, err := newClaudeAdapter().Apply(Options{Home: home, BaseURL: "http://h:1", APIKey: "k", Model: "m"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readFixture(t, path); strings.Contains(got, "CLAUDE_CODE_AUTO_COMPACT_WINDOW") {
		t.Errorf("no limits resolved, yet the window was written:\n%s", got)
	}
}

func TestOpenCodeWritesModelLimit(t *testing.T) {
	home := t.TempDir()
	path := opencodePaths(home).Files[0].Path
	// The JSONC patcher splices into an existing document, so the file starts
	// from the config the client itself leaves behind.
	writeFixture(t, path, "{\n  \"$schema\": \"https://opencode.ai/config.json\"\n}\n")
	opts := Options{
		Home: home, BaseURL: "http://h:1", APIKey: "k", Model: "glm/glm-5.3",
		ModelLimits: map[string]ModelLimits{"glm/glm-5.3": {ContextWindow: 200000, MaxOutput: 32000}},
	}
	if _, err := newOpenCodeAdapter().Apply(opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := readFixture(t, path)
	if !strings.Contains(got, `"limit": {"context": 200000, "output": 32000}`) {
		t.Errorf("expected the model limit in the provider block, got:\n%s", got)
	}
}

func TestOpenCodeWithoutLimitsKeepsBareModelEntry(t *testing.T) {
	home := t.TempDir()
	path := opencodePaths(home).Files[0].Path
	writeFixture(t, path, "{\n  \"$schema\": \"https://opencode.ai/config.json\"\n}\n")
	if _, err := newOpenCodeAdapter().Apply(Options{Home: home, BaseURL: "http://h:1", APIKey: "k", Model: "m"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := readFixture(t, path)
	if strings.Contains(got, `"limit"`) {
		t.Errorf("no limits resolved, yet a limit was written:\n%s", got)
	}
	if !strings.Contains(got, `"m": {"name": "m"}}`) {
		t.Errorf("expected the bare model entry, got:\n%s", got)
	}
}

func TestCrushAndPiPreferResolvedLimits(t *testing.T) {
	home := t.TempDir()
	opts := Options{
		Home: home, BaseURL: "http://h:1", APIKey: "k", Model: "glm/glm-5.3",
		ModelLimits: map[string]ModelLimits{"glm/glm-5.3": {ContextWindow: 200000, MaxOutput: 32000}},
	}

	if _, err := newCrushAdapter().Apply(opts); err != nil {
		t.Fatalf("crush Apply: %v", err)
	}
	crush := readFixture(t, crushPaths(home).Files[0].Path)
	if !strings.Contains(crush, `"context_window": 200000`) {
		t.Errorf("crush expected the resolved window, got:\n%s", crush)
	}

	if _, err := newPiAdapter().Apply(opts); err != nil {
		t.Fatalf("pi Apply: %v", err)
	}
	pi := readFixture(t, piPaths(home).Files[0].Path)
	if !strings.Contains(pi, `"contextWindow": 200000`) || !strings.Contains(pi, `"maxTokens": 32000`) {
		t.Errorf("pi expected the resolved limits, got:\n%s", pi)
	}

	// A model the gateway knows only partially keeps the default for the
	// field it did not publish.
	partial := Options{
		Home: home, BaseURL: "http://h:1", APIKey: "k", Model: "glm/glm-5.3",
		ModelLimits: map[string]ModelLimits{"glm/glm-5.3": {ContextWindow: 200000}},
	}
	if _, err := newPiAdapter().Apply(partial); err != nil {
		t.Fatalf("pi partial Apply: %v", err)
	}
	piPartial := readFixture(t, piPaths(home).Files[0].Path)
	if !strings.Contains(piPartial, `"maxTokens": 16384`) {
		t.Errorf("pi expected the default maxTokens for an unknown output cap, got:\n%s", piPartial)
	}
}

func TestOmpWritesModelLimitsEntry(t *testing.T) {
	home := t.TempDir()
	path := ompPaths(home).Files[0].Path
	// omp merges into an existing providers table; the YAML patcher splices
	// into a document, so the file starts from the shape a previous connect
	// (or the client itself) leaves behind.
	writeFixture(t, path, "providers:\n  \"9router\":\n    baseUrl: http://old\n")
	opts := Options{
		Home: home, BaseURL: "http://h:1", APIKey: "k", Model: "glm/glm-5.3",
		ModelLimits: map[string]ModelLimits{"glm/glm-5.3": {ContextWindow: 200000, MaxOutput: 32000}},
	}
	if _, err := newOmpAdapter().Apply(opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := readFixture(t, path)
	for _, want := range []string{"contextWindow: 200000", "maxTokens: 32000", "discovery: {type: proxy}"} {
		if !strings.Contains(got, want) {
			t.Errorf("after Apply, missing %q\ngot:\n%s", want, got)
		}
	}
	// A second Apply must not stack a second models entry.
	if _, err := newOmpAdapter().Apply(opts); err != nil {
		t.Fatalf("Apply 2: %v", err)
	}
	if n := strings.Count(readFixture(t, path), "contextWindow: 200000"); n != 1 {
		t.Errorf("models entry appears %d times, want 1:\n%s", n, readFixture(t, path))
	}
}

func TestOmpWithoutLimitsKeepsPreviousShape(t *testing.T) {
	home := t.TempDir()
	path := ompPaths(home).Files[0].Path
	writeFixture(t, path, "providers:\n  \"9router\":\n    baseUrl: http://old\n")
	if _, err := newOmpAdapter().Apply(Options{Home: home, BaseURL: "http://h:1", APIKey: "k", Model: "m"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := readFixture(t, path)
	if strings.Contains(got, "contextWindow") || strings.Contains(got, "models") {
		t.Errorf("no limits resolved, yet models were written:\n%s", got)
	}
	if !strings.Contains(got, "discovery: {type: proxy}") {
		t.Errorf("expected the unchanged discovery shape, got:\n%s", got)
	}
}
