package dashboard

import (
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Auto-sync for the jailbreak preset library.
//
// The GitHub path in jailbreak_import.go answers "give me these prompts now":
// the operator names a repository, the importer writes one preset per file, and
// the story ends there. That is right for a one-shot fill and wrong for a
// collection whose prompts change — a new model file appears, a prompt is
// rewritten, and the operator's library has no way to hear about it. This file
// turns the same fetch into a standing subscription: a source is a repository
// the operator follows, and a pass re-reads every file it lists.
//
// Three rules keep it safe to run unattended:
//
//   - Only presets this worker wrote are rewritten. A preset carries its origin
//     (JailbreakDoc.SyncOrigin), and a pass touches a preset only when that
//     origin names the source it came from. A preset typed, uploaded or
//     imported by hand has no origin, so every pass is invisible to it — which
//     is what stops a background write from reverting an operator's edit.
//   - Nothing upstream is ever deleted. A file that disappears from the
//     repository leaves its preset in place and is reported as orphaned; the
//     operator removes it by hand. A subscription that silently deleted rows
//     could not be undone by reading a log.
//   - Every source is opt-in and removable. The seeded default follows
//     togg53192-cmd/jailbreaks so a fresh install has a library worth looking
//     at, and an operator who wants none of it deletes the source and the
//     presets stay — sync only ever adds and rewrites, it never clears.
//
// Naming: a preset id is derived from the repository and file, in the
// owner_repo-model form the operator asked for, so "togg53192-cmd/jailbreaks
// → DeepSeek/deepseek v4.md" becomes the preset
// "togg53192-cmd_jailbreaks-deepseek-v4" and two collections that both carry a
// deepseek file do not collide. A file already following the convention keeps
// its own name.

// jailbreakSyncScope is the kv scope holding the operator's sync sources.
const jailbreakSyncScope = "jailbreak_sync"

// jailbreakSyncSourceKey is the kv key of the source list. The whole list is
// one JSON document rather than one row per source: it is read and written as a
// unit by the worker, and a list the operator can reorder is one value.
const jailbreakSyncSourceKey = "sources"

// Seeded on first read so a fresh install has a library worth looking at. The
// operator can delete it, and deleting it changes nothing about the presets it
// already wrote.
func defaultJailbreakSyncSources() []JailbreakSyncSource {
	return []JailbreakSyncSource{{
		ID:      "jailbreaks-default",
		Target:  "togg53192-cmd/jailbreaks",
		Enabled: true,
	}}
}

// JailbreakSyncSource is one repository the preset library follows.
type JailbreakSyncSource struct {
	ID string `json:"id"`
	// Target is anything parseJailbreakImportTarget accepts: an
	// "owner/repo" shorthand, a GitHub URL, or a raw file URL.
	Target string `json:"target"`
	// Ref pins a branch, tag or sha. Empty follows the repository default,
	// which is what an operator who does not care about versions wants.
	Ref string `json:"ref,omitempty"`
	// Enabled is the operator's switch. A disabled source is kept and never
	// fetched, so pausing a collection is not the same as forgetting it.
	Enabled bool `json:"enabled"`
	// Name is the label the dashboard shows. Derived from the target when the
	// operator does not give one, so a source added by hand still reads well in
	// a list.
	Name string `json:"name,omitempty"`
	// LastSyncAt / LastError report the previous pass, so the panel can show
	// "synced 4 minutes ago" or the reason a fetch keeps failing without the
	// operator opening a log.
	LastSyncAt string `json:"lastSyncAt,omitempty"`
	LastError  string `json:"lastError,omitempty"`
	// LastResult is the summary of the most recent pass: what it wrote, what it
	// rewrote, and what upstream no longer lists.
	LastResult *JailbreakSyncResult `json:"lastResult,omitempty"`
}

// JailbreakSyncResult is what one pass did, per source.
type JailbreakSyncResult struct {
	Written  int `json:"written"`
	Updated  int `json:"updated"`
	Orphaned int `json:"orphaned"`
	Failed   int `json:"failed"`
	// Files is the per-file detail: each entry names the file, the preset it
	// resolved to, and what the pass did with it.
	Files []JailbreakSyncFile `json:"files,omitempty"`
}

// JailbreakSyncFile is one file of one pass.
type JailbreakSyncFile struct {
	Path   string `json:"path"`
	Preset string `json:"preset,omitempty"`
	Action string `json:"action"` // "written", "updated", "unchanged", "orphaned", "failed"
	Reason string `json:"reason,omitempty"`
}

// jailbreakSyncStore is the persistence seam. Everything the worker needs from
// the database is behind it, so a test drives the same code path with an
// in-memory store and never opens SQLite.
type jailbreakSyncStore interface {
	// ListSources / SaveSources read and write the operator's subscriptions.
	ListSources(ctx context.Context) ([]JailbreakSyncSource, error)
	SaveSources(ctx context.Context, sources []JailbreakSyncSource) error
	// Presets lists the whole library, so a pass can tell which presets it owns
	// and which files upstream no longer lists.
	Presets(ctx context.Context) ([]*db.JailbreakDoc, error)
	// SavePreset writes one preset, owned by the source named in origin.
	SavePreset(ctx context.Context, preset *db.JailbreakDoc) error
	// FetchFiles lists the files of one source, in repository order, and reports
	// the ones the caps dropped. The skip list is part of the contract, not a
	// side channel: a 337-file collection contributes 195, and a pass that
	// discarded the other 142 silently would leave the operator believing the
	// collection is fully synced when two thirds of it was capped.
	FetchFiles(ctx context.Context, src JailbreakSyncSource) ([]githubFile, []jailbreakImportRefusal, string, error)
	// FetchText reads one file's body at the ref the pass resolved.
	FetchText(ctx context.Context, src JailbreakSyncSource, ref, path string) (string, error)
}

// jailbreakSync is the worker: one pass over every enabled source, guarded so
// two passes never overlap.
type jailbreakSync struct {
	store  jailbreakSyncStore
	client *http.Client

	mu      sync.Mutex
	running bool
	// lastPassAt gates the periodic loop, which is time-based rather than
	// ticker-based: a pass that takes a long time must not queue another.
	lastPassAt time.Time
}

// JailbreakSyncInterval is how often the periodic loop runs. A prompt
// collection changes on the order of days; every hour is frequent enough to
// feel live and cheap enough to ignore on a one-vCPU box.
const JailbreakSyncInterval = time.Hour

// NewJailbreakSync builds the production worker over the real repository.
func NewJailbreakSync(repo *db.Repo) *jailbreakSync {
	return &jailbreakSync{
		store:  &jailbreakSyncRepoStore{repo: repo},
		client: &http.Client{Timeout: jailbreakImportTimeout},
	}
}

// Run executes one pass over every enabled source and returns what it did.
// It is safe to call concurrently: a second caller while a pass runs gets
// ErrJailbreakSyncBusy rather than two passes racing.
var ErrJailbreakSyncBusy = fmt.Errorf("a preset sync is already running")

func (s *jailbreakSync) Run(ctx context.Context) ([]JailbreakSyncOutcome, error) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil, ErrJailbreakSyncBusy
	}
	s.running = true
	s.lastPassAt = time.Now()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	sources, err := s.store.ListSources(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sync sources: %w", err)
	}
	outcomes := make([]JailbreakSyncOutcome, 0, len(sources))
	// The per-source status is folded back into the list that is written, so
	// the next read of the sources — the panel, or the next pass — sees the
	// timestamp and the error the operator needs. runOne takes its source by
	// value (it is a snapshot, mutated only inside the pass), so the outcome is
	// the only place the result lands.
	updated := make(map[string]JailbreakSyncSource, len(sources))
	for _, src := range sources {
		if !src.Enabled {
			continue
		}
		outcome := s.runOne(ctx, src)
		outcomes = append(outcomes, outcome)
		updated[outcome.Source.ID] = outcome.Source
	}
	for i := range sources {
		if folded, ok := updated[sources[i].ID]; ok {
			sources[i] = folded
		}
	}
	// The write-back happens even when a pass failed partway: a source that
	// looks like it never ran is the one failure the operator cannot diagnose
	// from the panel.
	if err := s.store.SaveSources(ctx, sources); err != nil {
		return outcomes, fmt.Errorf("save sync sources: %w", err)
	}
	return outcomes, nil
}

// JailbreakSyncOutcome is one source's pass, as the worker reports it.
type JailbreakSyncOutcome struct {
	Source JailbreakSyncSource `json:"source"`
	Result JailbreakSyncResult `json:"result"`
}

// runOne syncs one source and folds the result back into the source record.
func (s *jailbreakSync) runOne(ctx context.Context, src JailbreakSyncSource) JailbreakSyncOutcome {
	result := JailbreakSyncResult{Files: make([]JailbreakSyncFile, 0)}
	fail := func(err error) JailbreakSyncOutcome {
		result.Failed++
		result.Files = append(result.Files, JailbreakSyncFile{Action: "failed", Reason: err.Error()})
		src.LastError = err.Error()
		src.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
		return JailbreakSyncOutcome{Source: src, Result: result}
	}

	files, skipped, ref, err := s.store.FetchFiles(ctx, src)
	if err != nil {
		return fail(err)
	}
	// A collection larger than the caps contributes only what fits, and the rest
	// is reported as failed with the reason: 195 of 337 written and no mention of
	// the other 142 is a library that silently stops at two thirds.
	for _, drop := range skipped {
		result.Failed++
		result.Files = append(result.Files, JailbreakSyncFile{
			Path: drop.Path, Action: "failed", Reason: drop.Reason,
		})
	}
	presets, err := s.store.Presets(ctx)
	if err != nil {
		return fail(err)
	}

	origin := jailbreakSyncOrigin(src, ref)
	// The presets this source owns, keyed by the path it wrote them from. The
	// set of paths upstream lists minus this set is what went missing.
	mine := make(map[string]*db.JailbreakDoc, len(files))
	for _, p := range presets {
		if p.SyncOrigin == origin && p.SyncPath != "" {
			mine[p.SyncPath] = p
		}
	}

	// The claimed-stem set stops a short file name from stealing a sibling's
	// model: "deepseek" must not claim the id "deepseek-v4" already holds.
	claimed := jailbreakClaimedStems(files)
	listed := make(map[string]bool, len(files))
	for _, f := range files {
		listed[f.Path] = true
		text, err := s.store.FetchText(ctx, src, ref, f.Path)
		if err != nil {
			result.Failed++
			result.Files = append(result.Files, JailbreakSyncFile{Path: f.Path, Action: "failed", Reason: err.Error()})
			continue
		}
		id := jailbreakSyncPresetID(src, f.Path)
		existing := mine[f.Path]
		action := "written"
		if existing != nil {
			action = "updated"
			// An unchanged body is not rewritten: an operator may have retitled
			// or rearranged the preset, and rewriting an identical prompt only
			// to bump UpdatedAt would mark their edit as "no change" and hide
			// when the prompt last actually moved.
			if existing.Prompt == text {
				action = "unchanged"
			}
		}
		if action == "unchanged" {
			result.Files = append(result.Files, JailbreakSyncFile{Path: f.Path, Preset: id, Action: action})
			continue
		}
		doc := &db.JailbreakDoc{
			ID:         id,
			Name:       jailbreakSyncPresetName(src, f.Path),
			Prompt:     text,
			Source:     fmt.Sprintf("%s@%s/%s", src.Target, ref, f.Path),
			Kind:       jailbreakImportKind(jailbreakFileStem(f.Path)),
			Keywords:   jailbreakImportKeywords(jailbreakFileStem(f.Path), claimed),
			SyncOrigin: origin,
			SyncPath:   f.Path,
			// A synced preset's arrangement place is the operator's to move, so
			// an update never writes Order: a preset the operator dragged to the
			// top of a long library must stay there.
		}
		if existing != nil {
			// Everything the operator owns is carried across: an update
			// rewrites the prompt, not the preset's title, its place in the
			// library, its creation date, or the display fields they edited.
			// The prompt, keywords and source are the only things upstream
			// owns.
			doc.Name = existing.Name
			doc.Order = existing.Order
			doc.CreatedAt = existing.CreatedAt
			doc.Keywords = mergeSyncKeywords(existing.Keywords, doc.Keywords)
			doc.Excludes = existing.Excludes
			doc.Position = existing.Position
			doc.Mode = existing.Mode
		}
		if err := s.store.SavePreset(ctx, doc); err != nil {
			result.Failed++
			result.Files = append(result.Files, JailbreakSyncFile{Path: f.Path, Action: "failed", Reason: err.Error()})
			continue
		}
		if action == "written" {
			result.Written++
		} else {
			result.Updated++
		}
		result.Files = append(result.Files, JailbreakSyncFile{Path: f.Path, Preset: id, Action: action})
	}

	// Anything this source owned that upstream no longer lists is orphaned, not
	// deleted: the operator decides.
	for p := range mine {
		if !listed[p] {
			result.Orphaned++
			result.Files = append(result.Files, JailbreakSyncFile{Path: p, Preset: mine[p].ID, Action: "orphaned"})
		}
	}

	sort.SliceStable(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	src.LastError = ""
	src.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
	last := result
	src.LastResult = &last
	return JailbreakSyncOutcome{Source: src, Result: result}
}

// jailbreakSyncOrigin is the value stored on a preset written by a pass of this
// source, so a later pass recognises its own work. The ref is part of it: a
// source pinned to a tag owns different presets from the same repository
// following main, and the two must not overwrite each other.
func jailbreakSyncOrigin(src JailbreakSyncSource, ref string) string {
	return strings.TrimSpace(src.Target) + "@" + ref
}

// jailbreakSyncPresetID derives a preset id from the repository and the file,
// in the "username_github-model-vX" convention the operator asked for:
// togg53192-cmd/jailbreaks → "togg53192-cmd_jailbreaks", and
// "DeepSeek/deepseek v4.md" → "deepseek-v4", so the full id is
// "togg53192-cmd_jailbreaks-deepseek-v4".
//
// A file that already follows the convention keeps its own name: a repository
// whose files are already "username_github-model-vN.md" must not end up with the
// owner twice, which would make the id unreadable and the preset unrecognisable
// in a binding dropdown.
func jailbreakSyncPresetID(src JailbreakSyncSource, filePath string) string {
	repo := jailbreakSyncRepoSlug(src.Target)
	stem := slugify(jailbreakFileStem(filePath))
	// The stem already opens with the repository's own token, in either spelling
	// of the join a file may use, so prefixing again would double it. Without
	// this, a collection whose files already follow the convention produces ids
	// like "me_repo-me_repo-model-v1".
	stemNorm := strings.ReplaceAll(stem, "_", "-")
	repoNorm := strings.ReplaceAll(repo, "_", "-")
	if strings.HasPrefix(stemNorm, repoNorm+"-") {
		return stem
	}
	return repo + "-" + stem
}

// jailbreakSyncRepoSlug turns "owner/repo" (or a full GitHub URL) into the
// "owner_repo" token used in a preset id, so the separator is stable whatever
// form the operator typed the target in.
//
// The separator is deliberate: the operator's convention joins the owner and the
// repository with an underscore ("username_github-model-vX"), which keeps a name
// that already carries hyphens — "giienew-9router-pro" — readable, and it means a
// hyphen inside a repository name can never be mistaken for the boundary between
// the repository and the model. A URL whose host carries no owner/repo pair has
// no repository to name, so its host is slugified whole.
func jailbreakSyncRepoSlug(target string) string {
	t := strings.TrimSpace(target)
	if i := strings.Index(t, "://"); i >= 0 {
		t = t[i+3:]
		if i := strings.Index(t, "/"); i >= 0 {
			t = t[i+1:]
		}
	}
	t = strings.TrimSuffix(t, ".git")
	// "owner/repo" is the GitHub shorthand; anything deeper is a path inside one,
	// and only the first two segments name the repository.
	if segs := strings.Split(t, "/"); len(segs) >= 2 && segs[0] != "" && segs[1] != "" {
		return slugify(segs[0]) + "_" + slugify(strings.TrimSuffix(segs[1], ".git"))
	}
	return slugify(strings.ReplaceAll(t, "/", "-"))
}

// jailbreakSyncPresetName is what the library list shows. The file's own name
// reads better than an id, and the repository is what tells two collections
// apart when both carry a deepseek file.
func jailbreakSyncPresetName(src JailbreakSyncSource, filePath string) string {
	return jailbreakFileStem(filePath)
}

// mergeSyncKeywords unions the keywords already stored with the ones the file
// name derives, so an operator's hand-added rule survives an update.
//
// The store's normalisation is the union, which is what a preset the operator
// extended needs: dropping their rule would silently un-arm a model they bound
// on the next pass. A keyword whose text was derived from the old file name is
// not distinguishable from one they added, so the safe direction is to keep
// both; an operator who wants a stale rule gone deletes it on the preset.
func mergeSyncKeywords(existing, derived []db.JailbreakKeyword) []db.JailbreakKeyword {
	if len(existing) == 0 {
		return derived
	}
	out := make([]db.JailbreakKeyword, 0, len(existing)+len(derived))
	out = append(out, existing...)
	seen := make(map[string]bool, len(existing))
	for _, kw := range existing {
		seen[strings.ToLower(strings.TrimSpace(kw.Text))] = true
	}
	for _, kw := range derived {
		low := strings.ToLower(strings.TrimSpace(kw.Text))
		if seen[low] {
			continue
		}
		seen[low] = true
		out = append(out, kw)
	}
	return out
}

// Start makes the worker periodic: a first pass shortly after boot, then one
// every JailbreakSyncInterval. A failing pass is logged and retried on the next
// tick, which is the same shape StartBackgroundCatalogSync uses for the model
// catalog: a pass that fails tells the operator why on the next attempt rather
// than silently stopping.
func (s *jailbreakSync) Start(ctx context.Context) {
	go func() {
		// Wait for the gateway to finish booting before hitting GitHub: a pass
		// that runs while the schema is still being applied fails for a reason
		// that has nothing to do with the repository.
		select {
		case <-ctx.Done():
			return
		case <-time.After(45 * time.Second):
		}

		if _, err := s.Run(ctx); err != nil && !errors.Is(err, ErrJailbreakSyncBusy) {
			log.Warn("jailbreak_sync", "initial preset sync failed", "error", err)
		}

		ticker := time.NewTicker(JailbreakSyncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.mu.Lock()
				idle := time.Since(s.lastPassAt) < JailbreakSyncInterval
				s.mu.Unlock()
				if idle {
					// A manual pass ran recently; the periodic one would only
					// re-read the same bytes.
					continue
				}
				if _, err := s.Run(ctx); err != nil && !errors.Is(err, ErrJailbreakSyncBusy) {
					log.Warn("jailbreak_sync", "periodic preset sync failed", "error", err)
				}
			}
		}
	}()
}

// jailbreakSyncRepoStore is the production store: the operator's sources live in
// the kv scope, the presets in the jailbreak scope, and the files come from the
// GitHub importer the dashboard already trusts.
type jailbreakSyncRepoStore struct {
	repo *db.Repo
}

func (s *jailbreakSyncRepoStore) ListSources(ctx context.Context) ([]JailbreakSyncSource, error) {
	raw, err := s.repo.GetKVContext(ctx, jailbreakSyncScope, jailbreakSyncSourceKey)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		// First read: seed the default so a fresh install has a library.
		srcs := defaultJailbreakSyncSources()
		if err := s.SaveSources(ctx, srcs); err != nil {
			return nil, err
		}
		return srcs, nil
	}
	var srcs []JailbreakSyncSource
	if err := json.Unmarshal([]byte(raw), &srcs); err != nil {
		return nil, fmt.Errorf("decode sync sources: %w", err)
	}
	return srcs, nil
}

func (s *jailbreakSyncRepoStore) SaveSources(ctx context.Context, sources []JailbreakSyncSource) error {
	raw, err := json.Marshal(sources)
	if err != nil {
		return fmt.Errorf("encode sync sources: %w", err)
	}
	return s.repo.SetKVContext(ctx, jailbreakSyncScope, jailbreakSyncSourceKey, string(raw))
}

func (s *jailbreakSyncRepoStore) Presets(ctx context.Context) ([]*db.JailbreakDoc, error) {
	return s.repo.ListJailbreakPresetsContext(ctx)
}

func (s *jailbreakSyncRepoStore) SavePreset(ctx context.Context, preset *db.JailbreakDoc) error {
	return s.repo.SaveJailbreakPresetContext(ctx, preset)
}

func (s *jailbreakSyncRepoStore) FetchFiles(ctx context.Context, src JailbreakSyncSource) ([]githubFile, []jailbreakImportRefusal, string, error) {
	parsed, err := parseJailbreakImportTarget(src.Target)
	if err != nil {
		return nil, nil, "", err
	}
	if parsed.Github == nil {
		return nil, nil, "", errJailbreakBadTarget
	}
	gh := *parsed.Github
	if src.Ref != "" {
		gh.Ref = src.Ref
	}
	files, skipped, ref, err := defaultJailbreakImporter.collectFiles(ctx, gh)
	return files, skipped, ref, err
}

// FetchText reads one file's body at the ref the pass resolved. The ref comes in
// from the caller rather than being guessed: the default branch resolved during
// the listing is the one whose files were chosen, and reading a file at "HEAD"
// instead can miss a rename that happened mid-pass — on a mirror or a fork, that
// is a 404 the operator reads as "this file has no content".
func (s *jailbreakSyncRepoStore) FetchText(ctx context.Context, src JailbreakSyncSource, ref, filePath string) (string, error) {
	parsed, err := parseJailbreakImportTarget(src.Target)
	if err != nil {
		return "", err
	}
	gh := *parsed.Github
	if ref == "" {
		ref = src.Ref
	}
	if ref != "" {
		gh.Ref = ref
	}
	text, err := defaultJailbreakImporter.fetchText(ctx, defaultJailbreakImporter.rawURL(gh, ref, filePath), maxJailbreakImportFileBytes)
	if err != nil {
		// A pinned ref that a raw mirror refuses is retried unqualified, because
		// that is how several proxies serve the same file: they resolve the ref
		// themselves and reject a branch name in the path.
		text, err = defaultJailbreakImporter.fetchText(ctx, defaultJailbreakImporter.rawURL(gh, "HEAD", filePath), maxJailbreakImportFileBytes)
	}
	return text, err
}

// HandleListJailbreakSyncSources lists the operator's preset sync sources.
// GET /api/jailbreak/sync/sources.
func (h *DashboardHandler) HandleListJailbreakSyncSources(w http.ResponseWriter, r *http.Request) {
	sources, err := h.syncStore().ListSources(r.Context())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"sources": sources})
}

// HandleSaveJailbreakSyncSources replaces the whole source list.
// PUT /api/jailbreak/sync/sources with {"sources":[…]}.
//
// The list is replaced rather than patched, so the panel can reorder and
// remove in one request. An entry with an empty target is refused rather than
// dropped silently: a source that never fetched and never said why is the
// failure mode this avoids.
func (h *DashboardHandler) HandleSaveJailbreakSyncSources(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	var req struct {
		Sources []JailbreakSyncSource `json:"sources"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	out := make([]JailbreakSyncSource, 0, len(req.Sources))
	for i, src := range req.Sources {
		src.Target = strings.TrimSpace(src.Target)
		src.Ref = strings.TrimSpace(src.Ref)
		if src.Target == "" {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("source %d has no target", i+1))
			return
		}
		if _, err := parseJailbreakImportTarget(src.Target); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("source %q: %v", src.Target, err))
			return
		}
		if src.ID == "" {
			src.ID = fmt.Sprintf("src-%d", time.Now().UnixNano())
		}
		out = append(out, src)
	}
	if err := h.syncStore().SaveSources(r.Context(), out); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"sources": out, "ok": true})
}

// HandleRunJailbreakSync runs one pass immediately and reports what it did.
// POST /api/jailbreak/sync/run — the "Sync now" button.
func (h *DashboardHandler) HandleRunJailbreakSync(w http.ResponseWriter, r *http.Request) {
	s := h.syncWorker()
	if s == nil {
		handlerutil.WriteJSONError(w, http.StatusServiceUnavailable, "preset sync is not configured")
		return
	}
	outcomes, err := s.Run(r.Context())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusConflict, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"outcomes": outcomes, "ok": true})
}

// StartJailbreakSync installs the process-wide preset sync worker and starts
// its loop. It is idempotent: a second call is a no-op, so the app's OnStart
// and a test that boots the same graph twice cannot start two loops that
// overwrite each other's source records.
func StartJailbreakSync(repo *db.Repo, ctx context.Context) *jailbreakSync {
	presetSyncOnce.Do(func() {
		presetSyncWorker = &jailbreakSync{store: &jailbreakSyncRepoStore{repo: repo}}
	})
	if presetSyncWorker == nil || ctx == nil {
		return presetSyncWorker
	}
	// The stored GitHub token is loaded before the first pass, so a restart does
	// not lose it and then fail the first pass with a 403 the operator already
	// fixed. The env var wins when both are set: a process started with
	// GITHUB_TOKEN expects the environment to stay in charge.
	if token, err := repo.GetKVContext(ctx, jailbreakSyncScope, jailbreakGithubTokenKey); err == nil && strings.TrimSpace(token) != "" {
		defaultJailbreakImporter.SetGithubToken(firstNonEmpty(strings.TrimSpace(token), strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))))
	}
	presetSyncWorker.Start(ctx)
	return presetSyncWorker
}

var (
	presetSyncOnce   sync.Once
	presetSyncWorker *jailbreakSync
)

// syncStore returns the store the source endpoints read and write. It is the
// running worker's store when one exists, so a save lands where the next pass
// reads from; with no loop started (a handler built outside a boot) it opens one
// over the same repository, which is what lets the panel manage sources before
// the background loop starts.
func (h *DashboardHandler) syncStore() jailbreakSyncStore {
	if presetSyncWorker != nil {
		return presetSyncWorker.store
	}
	return &jailbreakSyncRepoStore{repo: h.Repo}
}

func (h *DashboardHandler) syncWorker() *jailbreakSync {
	if h.jailbreakSync != nil {
		return h.jailbreakSync
	}
	return presetSyncWorker
}

// jailbreakGithubTokenKey is the kv key of the GitHub token the operator pasted
// into the persona page. It lives beside the sync sources rather than in the
// settings schema, because it is a credential for one feature and the settings
// row would be a wider surface for it than the persona card needs.
const jailbreakGithubTokenKey = "githubToken"

// HandleGetJailbreakGithub reports whether a GitHub token is in force, and where
// it came from. GET /api/jailbreak/github.
//
// The token itself is never returned: a panel that needs to show "token set"
// does not need the value, and a response body carrying it would land in browser
// storage, in screenshots and in the request log. The source field is what tells
// the operator which of the two knobs to turn — the env var, or the field.
func (h *DashboardHandler) HandleGetJailbreakGithub(w http.ResponseWriter, r *http.Request) {
	envToken := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")) != ""
	stored, err := h.Repo.GetKVContext(r.Context(), jailbreakSyncScope, jailbreakGithubTokenKey)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	stored = strings.TrimSpace(stored)
	apiBase := defaultJailbreakImporter.apiBase
	cached, cacheMax := defaultJailbreakImporter.CacheStats()
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"tokenSet": stored != "" || envToken,
		// What the conditional-request cache is holding, so the panel can show
		// that a second pass with no upstream change is costing nothing.
		"cached":   cached,
		"cacheMax": cacheMax,
		"source": func() string {
			if stored != "" {
				return "dashboard"
			}
			if envToken {
				return "environment"
			}
			return "none"
		}(),
		// The mirror in force, so a panel can show that requests are being sent
		// somewhere other than api.github.com before the operator wonders why a
		// repository is unreachable.
		"apiBase":  apiBase,
		"rawBase":  defaultJailbreakImporter.rawBase,
		"envToken": envToken,
		"envMirror": strings.TrimSpace(os.Getenv("GITHUB_API_MIRROR")) != "" ||
			strings.TrimSpace(os.Getenv("GITHUB_RAW_MIRROR")) != "",
	})
}

// HandleSaveJailbreakGithubToken stores or clears the GitHub token.
// PUT /api/jailbreak/github/token with {"token":"ghp_…"} — an empty string
// clears it and falls back to the env var.
//
// Clearing is the important case: an operator who pastes a token and later
// revokes it has to be able to take it back out, and a panel that could only
// write would leave a dead credential in place reporting itself as active.
func (h *DashboardHandler) HandleSaveJailbreakGithubToken(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8*1024))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	token := strings.TrimSpace(req.Token)
	if err := h.Repo.SetKVContext(r.Context(), jailbreakSyncScope, jailbreakGithubTokenKey, token); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The importer in force picks it up on its next request, so the quota change
	// is visible without a restart. The env var still wins for a process that
	// has one: an operator who set GITHUB_TOKEN expects it to stay in charge.
	defaultJailbreakImporter.SetGithubToken(firstNonEmpty(token, strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))))
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "tokenSet": token != ""})
}
