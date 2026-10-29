package media

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
		"grok-build", "devin", "pi", "omp",
		"crush", "forge", "smelt", "codewhale",
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

func TestCLIDetectors_ClaudeHas9Router(t *testing.T) {
	t.Run("env ANTHROPIC_BASE_URL", func(t *testing.T) {
		t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:20130/v1")
		t.Setenv("NINEROUTER_URL", "")
		if !claudeHas9Router() {
			t.Fatal("expected claudeHas9Router to be true when ANTHROPIC_BASE_URL is set")
		}
	})

	t.Run("env NINEROUTER_URL", func(t *testing.T) {
		t.Setenv("ANTHROPIC_BASE_URL", "")
		t.Setenv("NINEROUTER_URL", "http://127.0.0.1:20130")
		if !claudeHas9Router() {
			t.Fatal("expected claudeHas9Router to be true when NINEROUTER_URL is set")
		}
	})

	t.Run("settings.json with ANTHROPIC_BASE_URL", func(t *testing.T) {
		t.Setenv("ANTHROPIC_BASE_URL", "")
		t.Setenv("NINEROUTER_URL", "")
		tmpHome := t.TempDir()
		t.Setenv("HOME", tmpHome)

		claudeDir := filepath.Join(tmpHome, ".claude")
		if err := os.MkdirAll(claudeDir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{"env": {"ANTHROPIC_BASE_URL": "http://127.0.0.1:20130/v1"}}`
		if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		if !claudeHas9Router() {
			t.Fatal("expected claudeHas9Router to be true from ~/.claude/settings.json")
		}
	})

	t.Run("settings.json with jsonc comments", func(t *testing.T) {
		t.Setenv("ANTHROPIC_BASE_URL", "")
		t.Setenv("NINEROUTER_URL", "")
		tmpHome := t.TempDir()
		t.Setenv("HOME", tmpHome)

		claudeDir := filepath.Join(tmpHome, ".claude")
		if err := os.MkdirAll(claudeDir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{
			// custom endpoint for 9router
			"env": {
				"ANTHROPIC_BASE_URL": "http://127.0.0.1:20130/v1",
			}
		}`
		if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		if !claudeHas9Router() {
			t.Fatal("expected claudeHas9Router to be true from JSONC settings.json")
		}
	})

	t.Run("unconfigured reports false", func(t *testing.T) {
		t.Setenv("ANTHROPIC_BASE_URL", "")
		t.Setenv("NINEROUTER_URL", "")
		tmpHome := t.TempDir()
		t.Setenv("HOME", tmpHome)
		t.Setenv("USERPROFILE", tmpHome)

		claudeDir := filepath.Join(tmpHome, ".claude")
		if err := os.MkdirAll(claudeDir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{"env": {}}`
		if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		if claudeHas9Router() {
			t.Fatal("expected claudeHas9Router to be false when unconfigured")
		}
	})
}

func TestCLIToolsHandler_AllStatuses_CarriesHas9Router(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:20130/v1")
	tmpBin := t.TempDir()
	fakeClaude := filepath.Join(tmpBin, "claude")
	if err := os.WriteFile(fakeClaude, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmpBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	h := NewCLIToolsHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/cli-tools/all-statuses", nil)
	h.HandleAllStatuses(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"has9Router":true`) && !strings.Contains(body, `"has9Router": true`) {
		t.Fatalf("expected all-statuses response to carry has9Router, got %s", body)
	}

	// When binary is not on PATH (like in a CI runner) but tool is configured in env,
	// it must still report installed: true and has9Router: true.
	t.Run("without binary on PATH but configured in env", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir()) // empty PATH
		t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:20130/v1")
		recEmptyPath := httptest.NewRecorder()
		h.HandleAllStatuses(recEmptyPath, httptest.NewRequest(http.MethodGet, "/api/cli-tools/all-statuses", nil))
		if !strings.Contains(recEmptyPath.Body.String(), `"has9Router":true`) && !strings.Contains(recEmptyPath.Body.String(), `"has9Router": true`) {
			t.Fatalf("expected has9Router true even without binary in PATH, got %s", recEmptyPath.Body.String())
		}
	})
}
