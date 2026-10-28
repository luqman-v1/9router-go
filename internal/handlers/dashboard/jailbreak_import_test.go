package dashboard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
)

// The target is operator input and the only thing standing between it and the
// gateway's own network, so every accepted shape is pinned and every rejected
// one is named.
func TestParseGithubJailbreakTarget(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		want      githubJailbreakSource
		wantURL   string
		wantErr   bool
		wantBadTg bool
	}{
		{
			name:   "shorthand owner repo",
			target: "togg53192-cmd/jailbreaks",
			want:   githubJailbreakSource{Owner: "togg53192-cmd", Repo: "jailbreaks"},
		},
		{
			name:   "shorthand with ref",
			target: "togg53192-cmd/jailbreaks@v2",
			want:   githubJailbreakSource{Owner: "togg53192-cmd", Repo: "jailbreaks", Ref: "v2"},
		},
		{
			name:   "web repo root",
			target: "https://github.com/togg53192-cmd/jailbreaks",
			want:   githubJailbreakSource{Owner: "togg53192-cmd", Repo: "jailbreaks"},
		},
		{
			name:   "web tree with ref only",
			target: "https://github.com/togg53192-cmd/jailbreaks/tree/main",
			want:   githubJailbreakSource{Owner: "togg53192-cmd", Repo: "jailbreaks", Ref: "main"},
		},
		{
			name:   "web tree with directory",
			target: "https://github.com/togg53192-cmd/jailbreaks/tree/main/prompts/deepseek",
			want:   githubJailbreakSource{Owner: "togg53192-cmd", Repo: "jailbreaks", Ref: "main", Path: "prompts/deepseek"},
		},
		{
			name:   "web blob names one file",
			target: "https://github.com/togg53192-cmd/jailbreaks/blob/main/deepseek%20v4.md",
			want:   githubJailbreakSource{Owner: "togg53192-cmd", Repo: "jailbreaks", Ref: "main", Path: "deepseek v4.md"},
		},
		{
			name:   "raw names one file",
			target: "https://raw.githubusercontent.com/togg53192-cmd/jailbreaks/main/deepseek%20v4.md",
			want:   githubJailbreakSource{Owner: "togg53192-cmd", Repo: "jailbreaks", Ref: "main", Path: "deepseek v4.md"},
		},
		{
			name:   "api repo root",
			target: "https://api.github.com/repos/togg53192-cmd/jailbreaks",
			want:   githubJailbreakSource{Owner: "togg53192-cmd", Repo: "jailbreaks"},
		},
		{
			name:   "api trees with ref",
			target: "https://api.github.com/repos/togg53192-cmd/jailbreaks/git/trees/main",
			want:   githubJailbreakSource{Owner: "togg53192-cmd", Repo: "jailbreaks", Ref: "main"},
		},
		{
			name:      "empty",
			target:    "   ",
			wantErr:   true,
			wantBadTg: true,
		},
		{
			name:      "owner only",
			target:    "togg53192-cmd",
			wantErr:   true,
			wantBadTg: true,
		},
		{
			name:      "three bare segments",
			target:    "owner/repo/path",
			wantErr:   true,
			wantBadTg: true,
		},
		{
			// Not GitHub, but a well-formed http(s) URL is a valid import
			// target now. The address guard runs at dial time, not here.
			name:    "non github host is a url target",
			target:  "https://evil.example.com/owner/repo",
			wantURL: "https://evil.example.com/owner/repo",
		},
		{
			name:    "internal host is a url target",
			target:  "http://127.0.0.1:8080/owner/repo",
			wantURL: "http://127.0.0.1:8080/owner/repo",
		},
		{
			name:      "path traversal in raw path",
			target:    "https://raw.githubusercontent.com/owner/repo/main/../../etc/passwd",
			wantErr:   true,
			wantBadTg: true,
		},
		{
			name:      "dot dot in ref",
			target:    "owner/repo@../..",
			wantErr:   true,
			wantBadTg: true,
		},
		{
			name:      "absolute path in shorthand ref",
			target:    "owner/repo@/etc/passwd",
			wantErr:   true,
			wantBadTg: true,
		},
		{
			name:      "bad characters in owner",
			target:    "own er/repo",
			wantErr:   true,
			wantBadTg: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseJailbreakImportTarget(tt.target)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseJailbreakImportTarget(%q) = %+v, want error", tt.target, got)
				}
				if tt.wantBadTg && !isBadTarget(err) {
					t.Fatalf("error %v is not marked as a bad target; the handler would answer 502 for an operator typo", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseJailbreakImportTarget(%q): %v", tt.target, err)
			}
			if tt.wantURL != "" {
				if got.URL != tt.wantURL || got.Github != nil {
					t.Fatalf("parseJailbreakImportTarget(%q) = %+v, want URL %q", tt.target, got, tt.wantURL)
				}
				return
			}
			if got.Github == nil || *got.Github != tt.want {
				t.Fatalf("parseJailbreakImportTarget(%q) = %+v, want github %+v", tt.target, got, tt.want)
			}
		})
	}
}

func isBadTarget(err error) bool {
	return errors.Is(err, errJailbreakBadTarget)
}

// The keyword chain is what makes an imported preset fire for the model it
// names, across providers, without the operator arming each id.
func TestJailbreakImportKeywords(t *testing.T) {
	tests := []struct {
		name    string
		stem    string
		claimed map[string]bool
		want    []string
	}{
		{
			name: "spaced family and version",
			stem: "deepseek v4",
			want: []string{"deepseek", "deepseek-v4"},
		},
		{
			name: "dot version is preserved because model ids carry it",
			stem: "glm-5.2",
			want: []string{"glm", "glm-5.2"},
		},
		{
			name: "three segments chain",
			stem: "Muse spark 1.3",
			want: []string{"muse", "muse-spark", "muse-spark-1.3"},
		},
		{
			name: "suffix that another file claims as its own name is dropped",
			stem: "kimi-k3-agents",
			// kimi-k3.md is in the same import and owns that id.
			claimed: map[string]bool{"kimi-k3": true, "kimi-k3-agents": true},
			want:    []string{"kimi", "kimi-k3-agents"},
		},
		{
			name: "single character segment is skipped",
			stem: "a-b-c",
			want: []string{"a-b", "a-b-c"},
		},
		{
			name: "unusable stem yields nothing",
			stem: "---",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jailbreakImportKeywords(tt.stem, tt.claimed)
			if len(got) != len(tt.want) {
				t.Fatalf("jailbreakImportKeywords(%q) = %+v, want %v", tt.stem, got, tt.want)
			}
			for i, kw := range got {
				if kw.Text != tt.want[i] {
					t.Fatalf("keyword[%d] = %q, want %q (all: %+v)", i, kw.Text, tt.want[i], got)
				}
				if kw.Mode != db.JailbreakKeywordWord {
					t.Fatalf("keyword[%d] mode = %q, want the default word mode", i, kw.Mode)
				}
			}
		})
	}
}

// Repository furniture is not a prompt: importing .github/workflows or the
// README that describes the collection would arm presets nobody asked for.
func TestJailbreakImportCandidate(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"deepseek v4.md", true},
		{"glm-5.2.md", true},
		{"grok-config.txt", true},
		{"prompts/deepseek v4.md", true},
		{"README.md", false},
		{"readme.markdown", false},
		{".github/workflows/ci.md", false},
		{".hidden/prompt.md", false},
		{"image.png", false},
		{"Makefile", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := jailbreakImportCandidate(tt.path); got != tt.want {
				t.Fatalf("jailbreakImportCandidate(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// fakeGithub serves the two endpoints the importer calls: the repository
// metadata for the default branch, the recursive tree, and the raw files.
type fakeGithub struct {
	api  *httptest.Server
	raw  *httptest.Server
	tree map[string]string // path -> body
}

func newFakeGithub(t *testing.T, files map[string]string) *fakeGithub {
	t.Helper()
	fg := &fakeGithub{tree: files}

	fg.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo":
			fmt.Fprint(w, `{"default_branch":"main"}`)
		case "/repos/owner/repo/git/trees/main":
			var b strings.Builder
			b.WriteString(`{"truncated":false,"tree":[`)
			first := true
			for p := range files {
				if !first {
					b.WriteByte(',')
				}
				first = false
				fmt.Fprintf(&b, `{"path":%q,"type":"blob","size":%d}`, p, len(files[p]))
			}
			b.WriteString(`]}`)
			fmt.Fprint(w, b.String())
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fg.api.Close)

	fg.raw = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A tree entry with no served body is a dead link: 404, so the import
		// reports it instead of storing an empty prompt.
		body, ok := files[strings.TrimPrefix(r.URL.Path, "/owner/repo/main/")]
		if !ok || body == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(fg.raw.Close)

	return fg
}

func (fg *fakeGithub) importer() *jailbreakImporter {
	imp := newJailbreakImporter()
	imp.apiBase = fg.api.URL
	imp.rawBase = fg.raw.URL
	// The fake is on loopback, which the production guard refuses. An operator
	// importing from their own network sets the same opt-in.
	imp.allowPrivate = true
	return imp
}

// The whole point of the import: a collection of one-file-per-model prompts
// becomes presets that resolve for the model they name, on any provider,
// without anyone arming an id.
func TestJailbreakImportFromGithubEndToEnd(t *testing.T) {
	fg := newFakeGithub(t, map[string]string{
		"deepseek v4.md":    "DEEPSEEK V4 RULE",
		"kimi-k3-agents.md": "KIMI K3 RULE",
		"README.md":         "# collection index, not a prompt",
	})
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	res, err := fg.importer().importPresets(t.Context(), repo, "owner/repo", "")
	if err != nil {
		t.Fatalf("importPresets: %v", err)
	}
	if res.Source != "owner/repo@main" {
		t.Fatalf("source = %q, want owner/repo@main", res.Source)
	}
	if len(res.Imported) != 2 {
		t.Fatalf("imported = %+v, want 2 presets", res.Imported)
	}
	if len(res.Refused) != 0 {
		t.Fatalf("refused = %+v, want none", res.Refused)
	}

	// A request for the model on a provider the collection never names still
	// resolves: the keyword chain covers the family.
	doc, err := repo.ResolveJailbreakDoc("providerx/deepseek-v4")
	if err != nil {
		t.Fatalf("resolve deepseek: %v", err)
	}
	if doc == nil || doc.Prompt != "DEEPSEEK V4 RULE" {
		t.Fatalf("resolved doc = %+v, want the deepseek v4 prompt", doc)
	}
	if doc.Source != "owner/repo@main/deepseek v4.md" {
		t.Fatalf("source = %q, want the github path it came from", doc.Source)
	}

	// The file carries a suffix the model id does not, so the chain has to
	// reach the shorter id for it to resolve at all.
	doc, err = repo.ResolveJailbreakDoc("kimi-k3")
	if err != nil {
		t.Fatalf("resolve kimi: %v", err)
	}
	if doc == nil || doc.Prompt != "KIMI K3 RULE" {
		t.Fatalf("resolved doc = %+v, want the kimi k3 prompt", doc)
	}

	// A model the collection does not cover stays untouched.
	doc, err = repo.ResolveJailbreakDoc("openai/gpt-4o")
	if err != nil {
		t.Fatalf("resolve unrelated: %v", err)
	}
	if doc != nil {
		t.Fatalf("resolved doc = %+v, want nothing for an uncovered model", doc)
	}

	// Re-importing the same collection updates in place rather than
	// duplicating, so a refresh is safe to run.
	if _, err := fg.importer().importPresets(t.Context(), repo, "owner/repo", ""); err != nil {
		t.Fatalf("second import: %v", err)
	}
	presets, err := repo.ListJailbreakPresets()
	if err != nil {
		t.Fatalf("list presets: %v", err)
	}
	if len(presets) != 2 {
		t.Fatalf("presets after re-import = %d, want 2", len(presets))
	}
}

// One unreachable file must not abort the rest: a collection with a dead link
// still imports everything it can, and says what it skipped.
func TestJailbreakImportReportsRefusals(t *testing.T) {
	fg := newFakeGithub(t, map[string]string{
		"good.md":    "GOOD RULE",
		"missing.md": "", // served as 404 by the fake
		"empty.md":   "   \n",
	})
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	res, err := fg.importer().importPresets(t.Context(), repo, "owner/repo", "")
	if err != nil {
		t.Fatalf("importPresets: %v", err)
	}
	if len(res.Imported) != 1 || res.Imported[0].ID != "good" {
		t.Fatalf("imported = %+v, want just good.md", res.Imported)
	}
	if len(res.Refused) != 2 {
		t.Fatalf("refused = %+v, want missing.md and empty.md", res.Refused)
	}
	reasons := map[string]string{}
	for _, r := range res.Refused {
		reasons[r.Path] = r.Reason
	}
	if !strings.Contains(reasons["missing.md"], "status 404") {
		t.Fatalf("missing.md reason = %q, want the 404", reasons["missing.md"])
	}
	if !strings.Contains(reasons["empty.md"], errJailbreakEmptyPrompt.Error()) {
		t.Fatalf("empty.md reason = %q, want the empty-prompt reason", reasons["empty.md"])
	}
}

// A single file target imports exactly that file, which is how an operator
// pulls one prompt out of a collection.
func TestJailbreakImportSingleFile(t *testing.T) {
	fg := newFakeGithub(t, map[string]string{
		"deepseek v4.md": "ONE FILE RULE",
		"other.md":       "OTHER RULE",
	})
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	res, err := fg.importer().importSource(t.Context(), repo, githubJailbreakSource{
		Owner: "owner", Repo: "repo", Ref: "main", Path: "deepseek v4.md",
	})
	if err != nil {
		t.Fatalf("importSource: %v", err)
	}
	if len(res.Imported) != 1 || res.Imported[0].Path != "deepseek v4.md" {
		t.Fatalf("imported = %+v, want only deepseek v4.md", res.Imported)
	}
	presets, err := repo.ListJailbreakPresets()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(presets) != 1 || presets[0].Prompt != "ONE FILE RULE" {
		t.Fatalf("presets = %+v, want the one file", presets)
	}
}

// A repository with no prompt files is an error, not a silent success.
func TestJailbreakImportNoCandidates(t *testing.T) {
	fg := newFakeGithub(t, map[string]string{"README.md": "# index only"})
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := fg.importer().importPresets(t.Context(), repo, "owner/repo", ""); err == nil {
		t.Fatal("importPresets succeeded on a repository with no prompt files")
	}
}

// The HTTP shell: a typo is the operator's to fix (400), and a working import
// answers with what it wrote.
func TestHandleImportJailbreak(t *testing.T) {
	fg := newFakeGithub(t, map[string]string{"deepseek v4.md": "HANDLER RULE"})
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	h := NewDashboardHandler(repo)
	h.jailbreakImport = fg.importer()
	r := setupTestRouterWithHandler(h)

	t.Run("missing target", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/jailbreak/import", bytes.NewReader([]byte(`{}`)))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("a non http scheme is 400, not a fetch attempt", func(t *testing.T) {
		payload, _ := json.Marshal(map[string]any{"target": "ftp://example.com/o/r"})
		req := httptest.NewRequest(http.MethodPost, "/api/jailbreak/import", bytes.NewReader(payload))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("an unreachable url is 502, the network's fault", func(t *testing.T) {
		payload, _ := json.Marshal(map[string]any{"target": "https://nonexistent.invalid/o/r"})
		req := httptest.NewRequest(http.MethodPost, "/api/jailbreak/import", bytes.NewReader(payload))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502 (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("import writes presets", func(t *testing.T) {
		payload, _ := json.Marshal(map[string]any{"target": "owner/repo"})
		req := httptest.NewRequest(http.MethodPost, "/api/jailbreak/import", bytes.NewReader(payload))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		var out struct {
			Ok       bool                     `json:"ok"`
			Source   string                   `json:"source"`
			Imported []jailbreakImportEntry   `json:"imported"`
			Refused  []jailbreakImportRefusal `json:"refused"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body.String())
		}
		if !out.Ok || len(out.Imported) != 1 || out.Imported[0].ID != "deepseek-v4" {
			t.Fatalf("response = %+v, want one imported deepseek-v4 preset", out)
		}
		if out.Refused == nil {
			t.Fatal("refused is null; a client cannot tell an empty list from a missing field")
		}
		doc, err := repo.GetJailbreakPreset("deepseek-v4")
		if err != nil {
			t.Fatalf("read preset: %v", err)
		}
		if doc == nil || doc.Prompt != "HANDLER RULE" {
			t.Fatalf("stored preset = %+v, want the imported prompt", doc)
		}
	})
}

// setupTestRouterWithHandler mounts the real route table on a handler the
// test built, so the import endpoint is exercised through the same wiring
// production uses rather than by calling the method directly.
func setupTestRouterWithHandler(h *DashboardHandler) chi.Router {
	r := chi.NewRouter()
	RegisterRoutes(r, h)
	return r
}

// uploadRequest builds a multipart body the way the browser's FormData does,
// so the endpoint is exercised through the same parser production uses.
func uploadRequest(t *testing.T, field, filename, content string) (*http.Request, *multipart.Writer) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/jailbreak/presets/upload", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req, mw
}

// An uploaded file is the third way to fill a preset, and it follows the same
// derivation as a GitHub import rather than inventing a second convention.
func TestHandleUploadJailbreakPreset(t *testing.T) {
	t.Run("writes the preset with the derived keyword chain", func(t *testing.T) {
		repo, cleanup := setupTestDB(t)
		defer cleanup()
		router := setupTestRouterWithHandler(NewDashboardHandler(repo))

		req, _ := uploadRequest(t, "file", "deepseek v4.md", "UPLOADED RULE")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		var out struct {
			Ok     bool                 `json:"ok"`
			Preset jailbreakImportEntry `json:"preset"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body.String())
		}
		if out.Preset.ID != "deepseek-v4" || out.Preset.Name != "deepseek v4" {
			t.Fatalf("preset = %+v, want id deepseek-v4 and name \"deepseek v4\"", out.Preset)
		}
		if strings.Join(out.Preset.Keywords, ",") != "deepseek,deepseek-v4" {
			t.Fatalf("keywords = %v, want the derived chain", out.Preset.Keywords)
		}
		doc, err := repo.GetJailbreakPreset("deepseek-v4")
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if doc == nil || doc.Prompt != "UPLOADED RULE" {
			t.Fatalf("stored preset = %+v, want the uploaded prompt", doc)
		}
		if doc.Source != "upload:deepseek v4.md" {
			t.Fatalf("source = %q, want the upload marker", doc.Source)
		}
		// And it resolves for the model it names, on any provider.
		resolved, err := repo.ResolveJailbreakDoc("providerx/deepseek-v4")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if resolved == nil || resolved.Prompt != "UPLOADED RULE" {
			t.Fatalf("resolved = %+v, want the uploaded prompt", resolved)
		}
	})

	t.Run("a client-supplied path cannot name the preset", func(t *testing.T) {
		repo, cleanup := setupTestDB(t)
		defer cleanup()
		router := setupTestRouterWithHandler(NewDashboardHandler(repo))

		req, _ := uploadRequest(t, "file", "../../etc/passwd.md", "TRAVERSAL RULE")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		presets, err := repo.ListJailbreakPresets()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(presets) != 1 || presets[0].ID != "passwd" {
			t.Fatalf("presets = %+v, want one preset named after the base name only", presets)
		}
	})

	t.Run("an empty file is refused with the reason", func(t *testing.T) {
		repo, cleanup := setupTestDB(t)
		defer cleanup()
		router := setupTestRouterWithHandler(NewDashboardHandler(repo))

		req, _ := uploadRequest(t, "file", "blank.md", "   \n\t")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
		}
		presets, err := repo.ListJailbreakPresets()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(presets) != 0 {
			t.Fatalf("presets = %+v, want nothing stored for an empty file", presets)
		}
	})

	t.Run("a missing file field is 400", func(t *testing.T) {
		repo, cleanup := setupTestDB(t)
		defer cleanup()
		router := setupTestRouterWithHandler(NewDashboardHandler(repo))

		req, _ := uploadRequest(t, "notfile", "deepseek v4.md", "IGNORED")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("a JSON body is not a file upload", func(t *testing.T) {
		repo, cleanup := setupTestDB(t)
		defer cleanup()
		router := setupTestRouterWithHandler(NewDashboardHandler(repo))

		req := httptest.NewRequest(http.MethodPost, "/api/jailbreak/presets/upload",
			strings.NewReader(`{"prompt":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("a file past the prompt ceiling is 413", func(t *testing.T) {
		repo, cleanup := setupTestDB(t)
		defer cleanup()
		router := setupTestRouterWithHandler(NewDashboardHandler(repo))

		big := strings.Repeat("x", maxJailbreakBodyBytes+1)
		req, _ := uploadRequest(t, "file", "huge.md", big)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413 (%s)", rec.Code, rec.Body.String())
		}
	})
}

// The import target is no longer GitHub-only: a bare owner/repo and a GitHub
// URL still walk a repository, while any other http(s) URL is one prompt file.
func TestParseJailbreakImportTarget(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		wantURL string
		wantGH  *githubJailbreakSource
		wantErr bool
	}{
		{
			name:   "shorthand is still a repository",
			target: "togg53192-cmd/jailbreaks",
			wantGH: &githubJailbreakSource{Owner: "togg53192-cmd", Repo: "jailbreaks"},
		},
		{
			name:   "github blob is a github file",
			target: "https://github.com/o/r/blob/main/SOUL.md",
			wantGH: &githubJailbreakSource{Owner: "o", Repo: "r", Ref: "main", Path: "SOUL.md"},
		},
		{
			name:    "any other https url is one file",
			target:  "https://paste.example.com/prompts/SOUL.md",
			wantURL: "https://paste.example.com/prompts/SOUL.md",
		},
		{
			name:    "plain http url is one file",
			target:  "http://files.example.com/PERSONA.md",
			wantURL: "http://files.example.com/PERSONA.md",
		},
		{
			name:    "url with a query still names its file",
			target:  "https://example.com/a/LOAD.md?raw=1",
			wantURL: "https://example.com/a/LOAD.md?raw=1",
		},
		{
			name:    "ftp scheme is refused",
			target:  "ftp://example.com/SOUL.md",
			wantErr: true,
		},
		{
			name:    "file scheme is refused",
			target:  "file:///etc/passwd",
			wantErr: true,
		},
		{
			name:    "scheme relative url is refused",
			target:  "//example.com/SOUL.md",
			wantErr: true,
		},
		{
			name:    "url with no host is refused",
			target:  "https:///SOUL.md",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseJailbreakImportTarget(tt.target)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseJailbreakImportTarget(%q) = %+v, want error", tt.target, got)
				}
				if !errors.Is(err, errJailbreakBadTarget) {
					t.Fatalf("error %v is not marked as a bad target", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseJailbreakImportTarget(%q): %v", tt.target, err)
			}
			if tt.wantURL != "" {
				if got.URL != tt.wantURL || got.Github != nil {
					t.Fatalf("got %+v, want URL %q", got, tt.wantURL)
				}
				return
			}
			if got.Github == nil || *got.Github != *tt.wantGH {
				t.Fatalf("got %+v, want github %+v", got, *tt.wantGH)
			}
		})
	}
}

// The kind label is what tells an instruction file apart from a prompt about a
// model, and it comes from the name the file already has.
func TestJailbreakImportKind(t *testing.T) {
	tests := []struct {
		stem string
		want string
	}{
		{"SOUL", "SOUL"},
		{"soul", "SOUL"},
		{"PERSONA", "PERSONA"},
		{"LOAD", "LOAD"},
		{"AGENTS", "AGENTS"},
		{"CLAUDE", "CLAUDE"},
		{"MODELFILE", "MODELFILE"},
		{"deepseek v4", ""},
		{"glm-5.2", ""},
		{"README", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.stem, func(t *testing.T) {
			if got := jailbreakImportKind(tt.stem); got != tt.want {
				t.Fatalf("jailbreakImportKind(%q) = %q, want %q", tt.stem, got, tt.want)
			}
		})
	}
}

// A URL that resolves into the gateway's own network must be refused: the
// target is operator input, and a fetcher that reaches the LAN turns the
// dashboard into a proxy for it.
func TestJailbreakImportPublicIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"2606:4700:4700::1111", true},
		{"127.0.0.1", false},
		{"127.8.9.9", false},
		{"10.0.0.5", false},
		{"172.16.4.4", false},
		{"172.31.255.255", false},
		{"192.168.1.1", false},
		{"169.254.169.254", false}, // cloud metadata
		{"100.64.0.1", false},      // CGNAT
		{"0.0.0.0", false},
		{"255.255.255.255", false},
		{"224.0.0.1", false},
		{"::1", false},
		{"fe80::1", false},
		{"fc00::1", false},
		{"::", false},
	}
	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			if got := jailbreakImportPublicIP(net.ParseIP(tt.ip)); got != tt.want {
				t.Fatalf("jailbreakImportPublicIP(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

// The guard has to bite at dial time, not only in a unit test of the
// predicate: a hostname that resolves to a loopback address is refused by the
// client itself, and the opt-in lifts it.
func TestJailbreakImportDialGuard(t *testing.T) {
	imp := newJailbreakImporter()
	imp.allowPrivate = false
	if err := imp.dialControl("tcp", "127.0.0.1:443", nil); err == nil {
		t.Fatal("dialControl accepted a loopback address")
	}
	if err := imp.dialControl("tcp", "8.8.8.8:443", nil); err != nil {
		t.Fatalf("dialControl refused a public address: %v", err)
	}
	imp.allowPrivate = true
	if err := imp.dialControl("tcp", "127.0.0.1:443", nil); err != nil {
		t.Fatalf("dialControl refused loopback with the opt-in set: %v", err)
	}
}

// An arbitrary URL is fetched through the same guarded client and becomes a
// preset named after the file at the end of it.
func TestJailbreakImportFromArbitraryURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prompts/SOUL.md" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/markdown")
		fmt.Fprint(w, "SOUL RULE FROM A URL")
	}))
	t.Cleanup(srv.Close)

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	imp := newJailbreakImporter()
	// The test server is on loopback, which the guard refuses by default; the
	// opt-in is what an operator importing from their own network sets.
	imp.allowPrivate = true
	res, err := imp.importPresets(t.Context(), repo, srv.URL+"/prompts/SOUL.md", "")
	if err != nil {
		t.Fatalf("importPresets: %v", err)
	}
	if len(res.Imported) != 1 {
		t.Fatalf("imported = %+v, want one preset", res.Imported)
	}
	entry := res.Imported[0]
	if entry.ID != "soul" || entry.Name != "SOUL" || entry.Kind != "SOUL" {
		t.Fatalf("entry = %+v, want id soul, name SOUL, kind SOUL", entry)
	}
	doc, err := repo.GetJailbreakPreset("soul")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if doc == nil || doc.Prompt != "SOUL RULE FROM A URL" {
		t.Fatalf("stored preset = %+v, want the fetched prompt", doc)
	}
	if doc.Source != "url:"+srv.URL+"/prompts/SOUL.md" {
		t.Fatalf("source = %q, want the url marker", doc.Source)
	}
}

// Without the opt-in the same loopback server is unreachable, which is the
// default an operator gets.
func TestJailbreakImportURLRefusesPrivateByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "SHOULD NOT BE FETCHED")
	}))
	t.Cleanup(srv.Close)

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	imp := newJailbreakImporter()
	imp.allowPrivate = false
	if _, err := imp.importPresets(t.Context(), repo, srv.URL+"/SOUL.md", ""); err == nil {
		t.Fatal("importPresets reached a loopback address without the opt-in")
	}
	presets, err := repo.ListJailbreakPresets()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(presets) != 0 {
		t.Fatalf("presets = %+v, want nothing stored", presets)
	}
}

// A page served as HTML is reduced to its text, so pasting a documentation URL
// does not store the markup as the prompt.
func TestJailbreakImportTextStripsHTML(t *testing.T) {
	body := []byte(`<html><head><title>t</title><style>p{color:red}</style>` +
		`<script>alert(1)</script></head><body><h1>Title</h1><p>First &amp; foremost</p>` +
		`<ul><li>one</li><li>two</li></ul><div>Tail</div></body></html>`)
	got := jailbreakImportText(body, "text/html; charset=utf-8")
	for _, want := range []string{"Title", "First & foremost", "one", "two", "Tail"} {
		if !strings.Contains(got, want) {
			t.Fatalf("text %q is missing %q", got, want)
		}
	}
	for _, unwanted := range []string{"<", "alert(1)", "color:red"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("text %q still carries %q", got, unwanted)
		}
	}

	// A plain body is kept verbatim.
	plain := jailbreakImportText([]byte("RAW PROMPT\n"), "text/plain")
	if plain != "RAW PROMPT\n" {
		t.Fatalf("plain text = %q, want it verbatim", plain)
	}
}

// A URL with no file name still names its preset, from the host.
func TestJailbreakURLName(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"https://example.com/a/SOUL.md", "SOUL.md"},
		{"https://example.com/a/b/PERSONA.md?x=1", "PERSONA.md"},
		{"https://paste.example.com/abc123", "abc123"},
		{"https://paste.example.com/", "paste.example.com"},
		{"https://paste.example.com", "paste.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := jailbreakURLName(tt.raw); got != tt.want {
				t.Fatalf("jailbreakURLName(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
