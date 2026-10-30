package media

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"9router/proxy/internal/clientconfig"
	"9router/proxy/internal/db"
	"9router/proxy/internal/dbtest"
	chathandlers "9router/proxy/internal/handlers/chat"
)

// The nine adapters were merged into internal/clientconfig but nothing served
// them over HTTP, so the dashboard's per-tool settings pages had no route to
// call. The handler resolves the client out of the path, which means the
// adapter table stays the single description of a client.
func TestCLIToolGetResolvesEveryAdapter(t *testing.T) {
	h := NewCLIToolHandler(nil)
	for _, id := range []string{"claude", "codex", "opencode", "droid", "crush", "kilo", "cline", "omp"} {
		t.Run(id, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/cli-tools/"+id+"-settings", nil)
			req.URL.Path = "/api/cli-tools/" + id + "-settings"
			rec := httptest.NewRecorder()
			h.HandleCLIToolGet(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("parse: %v", err)
			}
			if resp["tool"] != id {
				t.Errorf("tool = %v, want %q", resp["tool"], id)
			}
			files, _ := resp["files"].([]any)
			if len(files) == 0 {
				t.Errorf("%s resolved to no files", id)
			}
		})
	}
}

// An alias the adapters accept must work here too: users type both spellings.
func TestCLIToolGetAcceptsAliases(t *testing.T) {
	h := NewCLIToolHandler(nil)
	for alias, want := range map[string]string{
		"claude-code": "claude",
		"kilocode":    "kilo",
		"factory":     "droid",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/cli-tools/"+alias+"-settings", nil)
		req.URL.Path = "/api/cli-tools/" + alias + "-settings"
		rec := httptest.NewRecorder()
		h.HandleCLIToolGet(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body = %s", alias, rec.Code, rec.Body.String())
		}
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp["tool"] != want {
			t.Errorf("%s resolved to %v, want %q", alias, resp["tool"], want)
		}
	}
}

// A typo must answer 404 with the accepted names, not silently configure
// nothing and report success.
func TestCLIToolGetRefusesUnknownTool(t *testing.T) {
	h := NewCLIToolHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/cli-tools/not-a-client-settings", nil)
	req.URL.Path = "/api/cli-tools/not-a-client-settings"
	rec := httptest.NewRecorder()
	h.HandleCLIToolGet(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "claude") {
		t.Errorf("error does not list accepted names: %s", rec.Body.String())
	}
}

// POST writes the client's real file. The test points the adapter at a temp
// home so no developer config is touched (schema matrix §9).
func TestCLIToolPostWritesTheClientFile(t *testing.T) {
	home := t.TempDir()
	h := NewCLIToolHandler(nil)

	body := `{"baseUrl":"http://localhost:20128","apiKey":"sk-test","model":"cc/claude-sonnet-5"}`
	req := httptest.NewRequest(http.MethodPost, "/api/cli-tools/claude-settings", strings.NewReader(body))
	req.URL.Path = "/api/cli-tools/claude-settings"
	req.URL.RawQuery = "home=" + home
	rec := httptest.NewRecorder()

	// The POST body carries no home, so the adapter resolves against the real
	// home. Point HOME at the temp dir for the duration of the test.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	h.HandleCLIToolPost(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// The adapter wrote under the temp home, not the developer's.
	written := false
	_ = filepath.Walk(home, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			written = true
		}
		return nil
	})
	if !written {
		t.Errorf("no file was written under the temp home %s", home)
	}
}

// A POST without a base URL is refused before anything reaches the disk.
func TestCLIToolPostRequiresBaseURL(t *testing.T) {
	h := NewCLIToolHandler(nil)
	req := httptest.NewRequest(http.MethodPost, "/api/cli-tools/codex-settings", strings.NewReader(`{"apiKey":"x"}`))
	req.URL.Path = "/api/cli-tools/codex-settings"
	rec := httptest.NewRecorder()
	h.HandleCLIToolPost(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// The GET response must not carry the credential: it is served to a reader that
// only asked what would be written.
func TestCLIToolGetDoesNotLeakTheAPIKey(t *testing.T) {
	h := NewCLIToolHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/cli-tools/claude-settings?apiKey=sk-secret-value", nil)
	req.URL.Path = "/api/cli-tools/claude-settings"
	req.URL.RawQuery = "apiKey=sk-secret-value"
	rec := httptest.NewRecorder()
	h.HandleCLIToolGet(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "sk-secret-value") {
		t.Errorf("GET leaked the API key: %s", rec.Body.String())
	}
}

// A client that has never run on this machine has no config directory. Plan
// refuses a base it cannot prove containment for, and that refusal reached the
// caller as a 500 — on the Linux CI runner every uninstalled client answered
// "base directory does not exist" instead of "not installed".
func TestCLIToolGetAnswersNotInstalledForAMissingConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	h := NewCLIToolHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/cli-tools/droid-settings", nil)
	req.URL.Path = "/api/cli-tools/droid-settings"
	rec := httptest.NewRecorder()
	h.HandleCLIToolGet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["installed"] != false {
		t.Errorf("installed = %v, want false when ~/.factory does not exist", resp["installed"])
	}
	// The paths are still reported: they are what Apply would create.
	files, _ := resp["files"].([]any)
	if len(files) == 0 {
		t.Error("files is empty; the caller cannot see where settings would go")
	}
}

// Reporting a client as installed just because Paths() returned a path was the
// bug: Paths resolves a location without touching the disk, so a clean machine
// looked fully provisioned. The flip side must still work — once the file
// exists, the client is installed.
func TestCLIToolGetInstalledFollowsTheConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	dir := filepath.Join(home, ".factory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewCLIToolHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/cli-tools/droid-settings", nil)
	req.URL.Path = "/api/cli-tools/droid-settings"
	rec := httptest.NewRecorder()
	h.HandleCLIToolGet(rec, req)

	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["installed"] != true {
		t.Errorf("installed = %v, want true once the config file exists", resp["installed"])
	}
}

// The adapter tests hand ModelLimits in directly, so this is the only place
// the matching against the real catalog runs. The entry is picked at runtime
// to pin the matching tiers without coupling to the registry's contents.
func TestAttachModelLimitsResolvesThroughTheCatalog(t *testing.T) {
	database, err := db.OpenDatabase(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := dbtest.CreateTables(database); err != nil {
		t.Fatalf("CreateTables: %v", err)
	}
	h := NewCLIToolHandler(chathandlers.NewChatHandler(db.NewRepo(database)))

	catalog := h.chat.BuildModelsListResult(context.Background(), chathandlers.ListModeCatalog)
	var id string
	var want clientconfig.ModelLimits
	for _, m := range catalog.Models {
		if m.ContextLength == nil || *m.ContextLength <= 0 {
			continue
		}
		id = m.ID
		want = clientconfig.ModelLimits{ContextWindow: *m.ContextLength}
		if m.MaxCompletionTokens != nil {
			want.MaxOutput = *m.MaxCompletionTokens
		}
		break
	}
	if id == "" {
		t.Fatal("catalog returned no model with a published context_length")
	}

	bare := id[strings.LastIndexByte(id, '/')+1:]
	opts := clientconfig.Options{}
	h.attachModelLimits(context.Background(), &opts,
		id, id+"(high)", bare, "definitely/not-a-model", "vendor/"+bare)

	if got := opts.ModelLimits[id]; got != want {
		t.Errorf("exact id %q = %+v, want %+v", id, got, want)
	}
	if got, ok := opts.ModelLimits[id+"(high)"]; !ok || got != want {
		t.Errorf("suffixed id %q(high) resolved %+v (present %v), want %+v", id, got, ok, want)
	}
	// First-wins may attach a sibling entry's limits when two models share a
	// bare name, so presence is the invariant here, not the value.
	if _, ok := opts.ModelLimits[bare]; !ok {
		t.Errorf("bare name %q did not resolve", bare)
	}
	// A slashed id the catalog does not carry stays unknown: the bare fallback
	// only answers ids typed without a prefix.
	for _, absent := range []string{"definitely/not-a-model", "vendor/" + bare} {
		if got, ok := opts.ModelLimits[absent]; ok {
			t.Errorf("unknown id %q resolved to %+v", absent, got)
		}
	}
}
