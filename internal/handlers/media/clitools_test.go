package media

import (
	"context"
	"errors"
	"testing"
)

func TestDetectAll_OneFailingDetectorIsNull(t *testing.T) {
	ctx := context.Background()
	v := "1.2.3"
	det := map[string]toolDetector{
		"ok": func(context.Context) (*cliStatus, error) {
			return &cliStatus{Installed: true, Version: &v}, nil
		},
		"bad": func(context.Context) (*cliStatus, error) {
			return nil, errors.New("boom")
		},
		"panic-tool": func(context.Context) (*cliStatus, error) {
			panic("probe threw")
		},
		"none": func(context.Context) (*cliStatus, error) {
			return &cliStatus{Installed: false}, nil
		},
	}

	res := detectAll(ctx, det)

	if res["bad"] != nil {
		t.Fatalf("failing detector should map to null, got %+v", res["bad"])
	}
	if res["panic-tool"] != nil {
		t.Fatalf("panicking detector should map to null, got %+v", res["panic-tool"])
	}
	if res["ok"] == nil || !res["ok"].Installed || res["ok"].Version == nil || *res["ok"].Version != v {
		t.Fatalf("ok tool mangled: %+v", res["ok"])
	}
	if res["none"] == nil || res["none"].Installed {
		t.Fatalf("not-installed tool mangled: %+v", res["none"])
	}
}

func TestCLIDetectors_HasAllToolIDs(t *testing.T) {
	// The tool ID set is load-bearing: it must match what the dashboard's
	// status card renders. A missing or renamed ID silently drops a card.
	want := []string{
		"claude", "codex", "opencode", "droid", "openclaw", "hermes",
		"cowork", "copilot", "cline", "kilo", "deepseek-tui", "jcode",
		"grok-build", "devin",
	}
	m := cliDetectors()
	for _, id := range want {
		if _, ok := m[id]; !ok {
			t.Errorf("missing tool detector for %q", id)
		}
	}
	if len(m) != len(want) {
		t.Errorf("got %d detectors, want %d", len(m), len(want))
	}
}