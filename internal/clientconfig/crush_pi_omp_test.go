package clientconfig

import (
	"os"
	"strings"
	"testing"
)

func TestCrushRoundTrip(t *testing.T) {
	home := t.TempDir()
	path := crushPaths(home).Files[0].Path
	writeFixture(t, path, `{"$schema": "x", "providers": {"other": {"type": "y"}}}`+"\n")

	opts := Options{Home: home, BaseURL: "http://h:1", APIKey: "k", Model: "m"}
	if _, err := newCrushAdapter().Apply(opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := readFixture(t, path)
	for _, want := range []string{`"openai-compat"`, `"base_url": "http://h:1/v1"`, `"api_key": "k"`, `"context_window": 128000`, `"other"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}

	if _, err := newCrushAdapter().Reset(Options{Home: home}); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	back := readFixture(t, path)
	if strings.Contains(back, `"9router"`) {
		t.Errorf("Reset left our provider:\n%s", back)
	}
	// crush keeps its file: §7.1 says reading it as a delete-file client is a
	// misreading of §6.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("crush deleted its file on reset: %v", err)
	}
	if !strings.Contains(back, `"other"`) {
		t.Errorf("Reset lost the user's provider:\n%s", back)
	}
}

func TestPiMergeKeepsForeignFields(t *testing.T) {
	home := t.TempDir()
	path := piPaths(home).Files[0].Path
	writeFixture(t, path, `{"providers": {"9router": {"userField": "keep-me"}}}`+"\n")

	opts := Options{Home: home, BaseURL: "http://h:1", APIKey: "k", Model: "m"}
	if _, err := newPiAdapter().Apply(opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := readFixture(t, path)
	// §3.8: pi merges rather than replaces, so a user field on the same entry
	// has to survive.
	if !strings.Contains(got, "keep-me") {
		t.Errorf("Apply replaced the provider entry instead of merging:\n%s", got)
	}
	for _, want := range []string{`"baseUrl": "http://h:1/v1"`, `"apiKey": "k"`, `"openai-completions"`, `"contextWindow": 128000`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

func TestOmpDeletesEmptyFileOnReset(t *testing.T) {
	home := t.TempDir()
	path := ompPaths(home).Files[0].Path
	writeFixture(t, path, "providers:\n  \"9router\":\n    baseUrl: http://old\n")

	if _, err := newOmpAdapter().Apply(Options{Home: home, BaseURL: "http://h:1", APIKey: "k"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readFixture(t, path); !strings.Contains(got, "http://h:1/v1") {
		t.Errorf("Apply did not write providers.9router:\n%s", got)
	}

	if _, err := newOmpAdapter().Reset(Options{Home: home}); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	// Nothing but an empty `providers:` was left, so §3.9 says delete the file.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("omp kept a file that held nothing but providers:; stat err = %v", err)
	}
}

// The delete-file branch must not fire when the user still has other config.
func TestOmpKeepsFileWithOtherKeys(t *testing.T) {
	home := t.TempDir()
	path := ompPaths(home).Files[0].Path
	writeFixture(t, path, "theme: dark\nproviders:\n  \"9router\":\n    baseUrl: http://old\n")

	if _, err := newOmpAdapter().Apply(Options{Home: home, BaseURL: "http://h:1", APIKey: "k"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := newOmpAdapter().Reset(Options{Home: home}); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	got := readFixture(t, path)
	if !strings.Contains(got, "theme: dark") {
		t.Errorf("Reset lost a key outside providers:\n%s", got)
	}
	if strings.Contains(got, "9router") {
		t.Errorf("Reset left our provider entry:\n%s", got)
	}
}
