package dashboard

import (
	"9router/proxy/internal/db"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSyncStore drives the worker without SQLite or GitHub. Every test builds
// one, seeds the files upstream "lists" and the presets the library holds, and
// reads back what the pass wrote.
type fakeSyncStore struct {
	sources []JailbreakSyncSource
	presets []*db.JailbreakDoc
	// files is what FetchFiles reports for the source under test.
	files []githubFile
	// skipped is what the caps dropped, reported to the pass exactly as the
	// importer reports it.
	skipped []jailbreakImportRefusal
	// texts maps a file path to its body.
	texts map[string]string
	// fetchErr makes FetchFiles fail, for the pass that must report rather
	// than write.
	fetchErr error
	// fetchTextErr makes FetchText fail for one path.
	fetchTextErr map[string]error
	// saveErr makes SavePreset fail.
	saveErr error
	// saved records every preset written, in order.
	saved []*db.JailbreakDoc
	// savedSources records the source write-back.
	savedSources []JailbreakSyncSource
}

func (f *fakeSyncStore) ListSources(context.Context) ([]JailbreakSyncSource, error) {
	return f.sources, nil
}

func (f *fakeSyncStore) SaveSources(_ context.Context, sources []JailbreakSyncSource) error {
	f.savedSources = sources
	return nil
}

func (f *fakeSyncStore) Presets(context.Context) ([]*db.JailbreakDoc, error) {
	return f.presets, nil
}

func (f *fakeSyncStore) SavePreset(_ context.Context, p *db.JailbreakDoc) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, p)
	return nil
}

func (f *fakeSyncStore) FetchFiles(context.Context, JailbreakSyncSource) ([]githubFile, []jailbreakImportRefusal, string, error) {
	if f.fetchErr != nil {
		return nil, nil, "", f.fetchErr
	}
	return f.files, f.skipped, "main", nil
}

func (f *fakeSyncStore) FetchText(_ context.Context, _ JailbreakSyncSource, _, path string) (string, error) {
	if f.fetchTextErr != nil {
		if err, ok := f.fetchTextErr[path]; ok {
			return "", err
		}
	}
	return f.texts[path], nil
}

func source() JailbreakSyncSource {
	return JailbreakSyncSource{ID: "s1", Target: "togg53192-cmd/jailbreaks", Enabled: true}
}

func newSync(s *fakeSyncStore) *jailbreakSync { return &jailbreakSync{store: s} }

func TestJailbreakSyncWritesPresetsWithOriginAndID(t *testing.T) {
	store := &fakeSyncStore{
		sources: []JailbreakSyncSource{source()},
		files:   []githubFile{{Path: "DeepSeek/deepseek v4.md"}},
		texts:   map[string]string{"DeepSeek/deepseek v4.md": "prompt body"},
	}
	s := newSync(store)
	outcomes, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %d, want 1", len(outcomes))
	}
	res := outcomes[0].Result
	if res.Written != 1 || res.Updated != 0 || res.Orphaned != 0 || res.Failed != 0 {
		t.Fatalf("written=%d updated=%d orphaned=%d failed=%d", res.Written, res.Updated, res.Orphaned, res.Failed)
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved %d presets, want 1", len(store.saved))
	}
	p := store.saved[0]
	// The id carries the repository and the file, in the operator's
	// username_github-model naming, so two collections that both have a
	// deepseek file do not collide.
	if want := "togg53192-cmd_jailbreaks-deepseek-v4"; p.ID != want {
		t.Errorf("preset id = %q, want %q", p.ID, want)
	}
	// Origin is what a later pass reads to recognise its own work, so a preset
	// the operator typed is never rewritten by sync.
	if want := "togg53192-cmd/jailbreaks@main"; p.SyncOrigin != want {
		t.Errorf("syncOrigin = %q, want %q", p.SyncOrigin, want)
	}
	if p.SyncPath != "DeepSeek/deepseek v4.md" {
		t.Errorf("syncPath = %q", p.SyncPath)
	}
	if p.Prompt != "prompt body" {
		t.Errorf("prompt = %q", p.Prompt)
	}
	if len(p.Keywords) == 0 {
		t.Error("keywords are derived from the file name so the preset covers the model without arming each id")
	}
	if outcomes[0].Source.LastError != "" {
		t.Errorf("source records an error after a clean pass: %q", outcomes[0].Source.LastError)
	}
}

func TestJailbreakSyncRewritesOnlyItsOwnPreset(t *testing.T) {
	// The library already holds one preset from this source and one the
	// operator wrote by hand. Only the source's own may be rewritten.
	store := &fakeSyncStore{
		sources: []JailbreakSyncSource{source()},
		files:   []githubFile{{Path: "m.md"}},
		texts:   map[string]string{"m.md": "new body"},
		presets: []*db.JailbreakDoc{
			{
				ID: "togg53192-cmd_jailbreaks-m", Prompt: "old body",
				SyncOrigin: "togg53192-cmd/jailbreaks@main", SyncPath: "m.md",
				Name: "Operator retitled this", Order: 7,
			},
			{ID: "hand-written", Prompt: "mine", Name: "My own"},
		},
	}
	s := newSync(store)
	if _, err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved %d presets, want 1 (only the source's own)", len(store.saved))
	}
	p := store.saved[0]
	if p.ID != "togg53192-cmd_jailbreaks-m" {
		t.Fatalf("rewrote %q", p.ID)
	}
	if p.Prompt != "new body" {
		t.Errorf("prompt = %q, want the fetched body", p.Prompt)
	}
	// The operator's arrangement and title survive: an update rewrites the
	// prompt, not the preset's place in the library.
	if p.Order != 7 {
		t.Errorf("order = %d, want 7 preserved", p.Order)
	}
	if p.Name != "Operator retitled this" {
		t.Errorf("name = %q, want the operator's kept", p.Name)
	}
}

func TestJailbreakSyncKeepsUnchangedPreset(t *testing.T) {
	store := &fakeSyncStore{
		sources: []JailbreakSyncSource{source()},
		files:   []githubFile{{Path: "m.md"}},
		texts:   map[string]string{"m.md": "same body"},
		presets: []*db.JailbreakDoc{
			{ID: "togg53192-cmd_jailbreaks-m", Prompt: "same body",
				SyncOrigin: "togg53192-cmd/jailbreaks@main", SyncPath: "m.md", UpdatedAt: "2020-01-01T00:00:00Z"},
		},
	}
	s := newSync(store)
	outcomes, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(store.saved) != 0 {
		t.Fatalf("rewrote an unchanged preset: %d writes", len(store.saved))
	}
	if res := outcomes[0].Result; res.Updated != 0 || res.Written != 0 {
		t.Errorf("updated=%d written=%d, want nothing written for an unchanged body", res.Updated, res.Written)
	}
}

func TestJailbreakSyncReportsOrphanInsteadOfDeleting(t *testing.T) {
	// Upstream dropped a file the source wrote. The preset stays, and the pass
	// says so: a subscription that deleted rows could not be undone by reading
	// a log.
	store := &fakeSyncStore{
		sources: []JailbreakSyncSource{source()},
		files:   []githubFile{{Path: "kept.md"}},
		texts:   map[string]string{"kept.md": "body"},
		presets: []*db.JailbreakDoc{
			{ID: "togg53192-cmd_jailbreaks-kept", Prompt: "body",
				SyncOrigin: "togg53192-cmd/jailbreaks@main", SyncPath: "kept.md"},
			{ID: "togg53192-cmd_jailbreaks-gone", Prompt: "stale",
				SyncOrigin: "togg53192-cmd/jailbreaks@main", SyncPath: "gone.md"},
			// A hand-written preset is never this source's business, orphan
			// rules included.
			{ID: "hand-written", Prompt: "mine"},
		},
	}
	s := newSync(store)
	outcomes, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	res := outcomes[0].Result
	if res.Orphaned != 1 {
		t.Errorf("orphaned = %d, want 1", res.Orphaned)
	}
	found := false
	for _, f := range res.Files {
		if f.Path == "gone.md" && f.Action == "orphaned" {
			found = true
		}
	}
	if !found {
		t.Error("the orphaned file is named in the result so the operator can decide")
	}
}

func TestJailbreakSyncFetchFailureIsReportedNotWritten(t *testing.T) {
	store := &fakeSyncStore{
		sources:  []JailbreakSyncSource{source()},
		fetchErr: errors.New("network unreachable"),
	}
	s := newSync(store)
	outcomes, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run must report a failed source rather than fail the pass: %v", err)
	}
	if len(store.saved) != 0 {
		t.Fatalf("wrote %d presets after a failed fetch", len(store.saved))
	}
	if outcomes[0].Source.LastError != "network unreachable" {
		t.Errorf("lastError = %q", outcomes[0].Source.LastError)
	}
}

func TestJailbreakSyncSkipsDisabledSource(t *testing.T) {
	off := source()
	off.Enabled = false
	store := &fakeSyncStore{
		sources: []JailbreakSyncSource{off},
		files:   []githubFile{{Path: "m.md"}},
		texts:   map[string]string{"m.md": "body"},
	}
	s := newSync(store)
	outcomes, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(outcomes) != 0 {
		t.Fatalf("a disabled source was synced: %d outcomes", len(outcomes))
	}
}

func TestJailbreakSyncBusyIsReported(t *testing.T) {
	store := &fakeSyncStore{
		sources: []JailbreakSyncSource{source()},
		files:   []githubFile{{Path: "m.md"}},
		texts:   map[string]string{"m.md": "body"},
	}
	s := newSync(store)
	// Simulate a pass in flight, the way the periodic loop meets a manual press.
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	if _, err := s.Run(context.Background()); !errors.Is(err, ErrJailbreakSyncBusy) {
		t.Fatalf("err = %v, want ErrJailbreakSyncBusy", err)
	}
}

func TestJailbreakSyncPresetID(t *testing.T) {
	cases := []struct {
		name   string
		target string
		path   string
		want   string
	}{
		{"owner prefix", "togg53192-cmd/jailbreaks", "DeepSeek/deepseek v4.md", "togg53192-cmd_jailbreaks-deepseek-v4"},
		{"url form", "https://github.com/giienew/9router-pro", "Opus v2.md", "giienew_9router-pro-opus-v2"},
		{"already prefixed", "me/repo", "me_repo-model-v1.md", "me-repo-model-v1"},
		{"underscore prefixed", "me/repo", "me-repo-model-v1.md", "me-repo-model-v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := jailbreakSyncPresetID(JailbreakSyncSource{Target: tc.target}, tc.path)
			if got != tc.want {
				t.Errorf("id = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJailbreakSyncOriginPinsTheRef(t *testing.T) {
	// The same repository followed on main and pinned to a tag owns different
	// presets; a pass of one must not touch the other's.
	got := jailbreakSyncOrigin(JailbreakSyncSource{Target: "me/repo"}, "v1")
	if got != "me/repo@v1" {
		t.Errorf("origin = %q", got)
	}
	if jailbreakSyncOrigin(JailbreakSyncSource{Target: "me/repo"}, "main") == jailbreakSyncOrigin(JailbreakSyncSource{Target: "me/repo"}, "v1") {
		t.Error("two refs share an origin, so their presets would overwrite each other")
	}
}

func TestJailbreakSyncRepoSlug(t *testing.T) {
	cases := map[string]string{
		"togg53192-cmd/jailbreaks":          "togg53192-cmd_jailbreaks",
		"https://github.com/me/repo":        "me_repo",
		"https://github.com/me/repo.git":    "me_repo",
		"https://raw.githubusercontent.com": "raw-githubusercontent-com",
	}
	for in, want := range cases {
		if got := jailbreakSyncRepoSlug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJailbreakSyncPresetNameReadsTheFileName(t *testing.T) {
	got := jailbreakSyncPresetName(source(), "Zhipu AI/glm-5-3-flash.md")
	if got != "glm-5-3-flash" {
		t.Errorf("name = %q", got)
	}
	if strings.Contains(got, "/") {
		t.Error("the name is the file's own stem, not its path")
	}
}
func TestMergeSyncKeywordsKeepsOperatorRules(t *testing.T) {
	existing := []db.JailbreakKeyword{{Text: "deepseek"}, {Text: "my-own-rule"}}
	derived := []db.JailbreakKeyword{{Text: "deepseek"}, {Text: "deepseek-v4"}}
	got := mergeSyncKeywords(existing, derived)
	seen := map[string]bool{}
	for _, kw := range got {
		seen[kw.Text] = true
	}
	if !seen["my-own-rule"] {
		t.Error("the operator's hand-added rule was dropped by an update")
	}
	if !seen["deepseek-v4"] {
		t.Error("a derived rule the store did not already hold was dropped")
	}
	if len(got) != 3 {
		t.Errorf("union produced %d keywords, want 3 (no duplicates)", len(got))
	}
}

func TestJailbreakSyncSourceEndpoints(t *testing.T) {
	conn, repo := newSyncTestRepo(t)
	defer conn.Close()
	h := NewDashboardHandler(repo)

	// A fresh database seeds the default source, so a new install has a library.
	rr := httptest.NewRecorder()
	h.HandleListJailbreakSyncSources(rr, httptest.NewRequest(http.MethodGet, "/api/jailbreak/sync/sources", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, body %s", rr.Code, rr.Body.String())
	}
	var listed struct {
		Sources []JailbreakSyncSource `json:"sources"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	found := false
	for _, s := range listed.Sources {
		if s.Target == "togg53192-cmd/jailbreaks" {
			found = true
		}
	}
	if !found {
		t.Fatalf("default source missing from %v", listed.Sources)
	}

	// The operator adds their own repository and disables the default.
	body := `{"sources":[{"id":"mine","target":"https://github.com/me/prompts","enabled":true},{"target":"togg53192-cmd/jailbreaks","enabled":false}]}`
	rr = httptest.NewRecorder()
	h.HandleSaveJailbreakSyncSources(rr, httptest.NewRequest(http.MethodPut, "/api/jailbreak/sync/sources", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("save status = %d, body %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.HandleListJailbreakSyncSources(rr, httptest.NewRequest(http.MethodGet, "/api/jailbreak/sync/sources", nil))
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Sources) != 2 {
		t.Fatalf("saved %d sources, want 2", len(listed.Sources))
	}
	if !listed.Sources[0].Enabled {
		t.Error("the operator's own source was saved disabled")
	}

	// A target the importer cannot parse is refused with 400, not stored.
	bad := `{"sources":[{"target":"not a repo"}]}`
	rr = httptest.NewRecorder()
	h.HandleSaveJailbreakSyncSources(rr, httptest.NewRequest(http.MethodPut, "/api/jailbreak/sync/sources", strings.NewReader(bad)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unparseable target status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "not a repo") {
		t.Errorf("the error names the offending target: %s", rr.Body.String())
	}
}

func TestJailbreakSyncRunEndpointBusyIsConflict(t *testing.T) {
	conn, repo := newSyncTestRepo(t)
	defer conn.Close()
	h := NewDashboardHandler(repo)
	StartJailbreakSync(repo, context.Background())
	s := h.syncWorker()
	if s == nil {
		t.Fatal("no sync worker behind the endpoints")
	}
	// Hold the worker busy the way an in-flight pass does, so the endpoint's
	// conflict answer is exercised without a real GitHub fetch.
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	rr := httptest.NewRecorder()
	h.HandleRunJailbreakSync(rr, httptest.NewRequest(http.MethodPost, "/api/jailbreak/sync/run", nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("busy status = %d, want 409", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), ErrJailbreakSyncBusy.Error()) {
		t.Errorf("body = %s", rr.Body.String())
	}
}

// newSyncTestRepo opens a private database with the real core schema, so the
// endpoint tests read and write the same tables a boot uses. It opens and
// bootstraps the file directly rather than going through InitGlobalDatabase:
// that is a process-wide singleton, which returns whatever an earlier test
// already opened (possibly closed), so a helper that called it would be
// asserting against another test's database.
func newSyncTestRepo(t *testing.T) (*sql.DB, *db.Repo) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jailbreak-sync.sqlite")
	conn, err := db.OpenDatabase(path)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.EnsureCoreSchema(conn); err != nil {
		conn.Close()
		t.Fatalf("schema: %v", err)
	}
	return conn, db.NewRepo(conn)
}

func TestJailbreakSyncPersistsSourceStatus(t *testing.T) {
	// The pass folds each source's result back into the list that is written.
	// Without that, the response is right and the database keeps the source's
	// pre-pass state — so the panel reads "not synced yet" forever on a source
	// that wrote 54 presets on boot.
	store := &fakeSyncStore{
		sources: []JailbreakSyncSource{source()},
		files:   []githubFile{{Path: "m.md"}},
		texts:   map[string]string{"m.md": "body"},
	}
	s := newSync(store)
	if _, err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(store.savedSources) != 1 {
		t.Fatalf("saved %d sources, want 1", len(store.savedSources))
	}
	saved := store.savedSources[0]
	if saved.LastSyncAt == "" {
		t.Error("the written source has no lastSyncAt, so the panel reports it never ran")
	}
	if saved.LastResult == nil {
		t.Fatal("the written source carries no lastResult, so the panel cannot say what the pass did")
	}
	if saved.LastResult.Written != 1 {
		t.Errorf("written = %d, want 1", saved.LastResult.Written)
	}
	// A disabled source is not synced, so it keeps what it had rather than
	// being stamped with a pass that never happened.
	off := source()
	off.Enabled = false
	off.ID = "off"
	store2 := &fakeSyncStore{sources: []JailbreakSyncSource{off}, files: []githubFile{{Path: "m.md"}}, texts: map[string]string{"m.md": "body"}}
	s2 := newSync(store2)
	if _, err := s2.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if store2.savedSources[0].LastSyncAt != "" {
		t.Error("a disabled source was stamped as synced")
	}
}

// A collection the caps trimmed must report what it dropped. Without this a
// 337-file repository contributes 195 and the operator reads the pass as a
// complete sync of two thirds of a library.
func TestJailbreakSyncReportsCappedFiles(t *testing.T) {
	store := &fakeSyncStore{
		sources: []JailbreakSyncSource{source()},
		files:   []githubFile{{Path: "kept.md", Type: "blob"}},
		texts:   map[string]string{"kept.md": "body"},
		skipped: []jailbreakImportRefusal{
			{Path: "huge.md", Reason: "file is 900000 bytes (max 524288)"},
			{Path: "late.md", Reason: "import is capped at 200 files"},
		},
	}
	s := newSync(store)
	outcomes, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	res := outcomes[0].Result
	if res.Failed != 2 {
		t.Errorf("failed = %d, want the two capped files", res.Failed)
	}
	for _, want := range []string{"huge.md", "late.md"} {
		found := false
		for _, f := range res.Files {
			if f.Path == want && f.Action == "failed" && f.Reason != "" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not reported with its reason", want)
		}
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved %d presets, want the one file that fit", len(store.saved))
	}
}

func TestJailbreakImportCandidateExtensions(t *testing.T) {
	// L1B3RT4S ships its whole library as `.mkd`. A filter that only knew .md and
	// .txt imported 1 file out of that repository's 44.
	cases := map[string]bool{
		"beam.md":         true,
		"beam.markdown":   true,
		"beam.mdown":      true,
		"beam.mkd":        true,
		"beam.txt":        true,
		"BEAM.MKD":        true,
		"jailbreaks/x.md": true,
		"beam.yaml":       false,
		"beam.json":       false,
		"beam":            false,
		"README.md":       false,
		"readme.txt":      false,
		".github/x.md":    false,
		"assets/logo.png": false,
	}
	for p, want := range cases {
		if got := jailbreakImportCandidate(p); got != want {
			t.Errorf("candidate(%q) = %v, want %v", p, got, want)
		}
	}
}

// The token is stored, reported as set, and never returned in the body. The
// last part is the point: a response that carries the credential puts it in
// browser storage, in screenshots and in the request log.
func TestJailbreakGithubTokenEndpoint(t *testing.T) {
	conn, repo := newSyncTestRepo(t)
	defer conn.Close()
	h := NewDashboardHandler(repo)

	// Before anything is saved, no token is in force.
	rr := httptest.NewRecorder()
	h.HandleGetJailbreakGithub(rr, httptest.NewRequest(http.MethodGet, "/api/jailbreak/github", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"tokenSet":false`) {
		t.Fatalf("body = %s, want tokenSet false", rr.Body.String())
	}

	t.Setenv("GITHUB_TOKEN", "")
	body := `{"token":"ghp_abc123"}`
	rr = httptest.NewRecorder()
	h.HandleSaveJailbreakGithubToken(rr, httptest.NewRequest(http.MethodPut, "/api/jailbreak/github/token", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("save status = %d, body %s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.HandleGetJailbreakGithub(rr, httptest.NewRequest(http.MethodGet, "/api/jailbreak/github", nil))
	got := rr.Body.String()
	if !strings.Contains(got, `"tokenSet":true`) || !strings.Contains(got, `"source":"dashboard"`) {
		t.Fatalf("body = %s, want a dashboard-sourced token", got)
	}
	if strings.Contains(got, "ghp_abc123") {
		t.Error("the token itself must never appear in a response body")
	}
	if defaultJailbreakImporter.GithubToken() != "ghp_abc123" {
		t.Errorf("the live importer did not pick up the token: %q", defaultJailbreakImporter.GithubToken())
	}

	// Clearing takes it back out, so a revoked credential is removable.
	rr = httptest.NewRecorder()
	h.HandleSaveJailbreakGithubToken(rr, httptest.NewRequest(http.MethodPut, "/api/jailbreak/github/token", strings.NewReader(`{"token":""}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("clear status = %d", rr.Code)
	}
	if defaultJailbreakImporter.GithubToken() != "" {
		t.Errorf("the live importer still holds a cleared token: %q", defaultJailbreakImporter.GithubToken())
	}
	rr = httptest.NewRecorder()
	h.HandleGetJailbreakGithub(rr, httptest.NewRequest(http.MethodGet, "/api/jailbreak/github", nil))
	if !strings.Contains(rr.Body.String(), `"tokenSet":false`) {
		t.Fatalf("body = %s, want tokenSet false after clearing", rr.Body.String())
	}
}

// The token rides the request to the configured API base — api.github.com, or
// the mirror an operator pointed apiBase at — and nowhere else. A mirror serves
// the same API and needs the same credential; a paste service the operator
// imported from must never receive it.
func TestJailbreakGithubTokenHeader(t *testing.T) {
	imp := newJailbreakImporter()
	imp.SetGithubToken("ghp_live")
	t.Setenv("GITHUB_TOKEN", "")

	cases := []struct {
		name string
		base string
		url  string
		want bool
	}{
		{"github api", "https://api.github.com", "https://api.github.com/repos/o/r", true},
		{"mirror", "https://gh-proxy.example.com", "https://gh-proxy.example.com/repos/o/r", true},
		{"mirror case-insensitive", "https://GH-Proxy.example.com", "https://gh-proxy.example.com/repos/o/r", true},
		{"other host", "https://api.github.com", "https://raw.githubusercontent.com/o/r/main/x.md", false},
		{"paste service", "https://api.github.com", "https://paste.example.com/abc", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			imp.apiBase = tc.base
			u, err := url.Parse(tc.url)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := sameHost(imp.apiBase, u)
			if got != tc.want {
				t.Errorf("sameHost(%q, %q) = %v, want %v", imp.apiBase, tc.url, got, tc.want)
			}
		})
	}
}

func TestJailbreakSyncPreservesEnabledChoice(t *testing.T) {
	on := true
	store := &fakeSyncStore{
		sources: []JailbreakSyncSource{source()},
		files:   []githubFile{{Path: "m.md"}},
		texts:   map[string]string{"m.md": "new body"},
		presets: []*db.JailbreakDoc{
			{
				ID: "togg53192-cmd_jailbreaks-m", Prompt: "old body",
				SyncOrigin: "togg53192-cmd/jailbreaks@main", SyncPath: "m.md",
				Enabled: &on,
			},
		},
	}
	s := newSync(store)
	if _, err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved %d presets, want 1", len(store.saved))
	}
	p := store.saved[0]
	if p.Enabled == nil || !*p.Enabled {
		t.Errorf("enabled = %v, want the operator's explicit on preserved", p.Enabled)
	}
}

func TestJailbreakSyncNewPresetStartsDisabled(t *testing.T) {
	store := &fakeSyncStore{
		sources: []JailbreakSyncSource{source()},
		files:   []githubFile{{Path: "m.md"}},
		texts:   map[string]string{"m.md": "new body"},
	}
	s := newSync(store)
	if _, err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved %d presets, want 1", len(store.saved))
	}
	p := store.saved[0]
	if p.Enabled != nil {
		t.Errorf("enabled = %v, want no explicit choice on a new synced preset", *p.Enabled)
	}
	if p.EffectiveEnabled() {
		t.Error("new synced preset is active, want it inert until switched on")
	}
}
