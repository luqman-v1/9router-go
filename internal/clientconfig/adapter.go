package clientconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// The adapter layer: one implementation per client in
// docs/specs/client-schema-matrix.md §1.1. An adapter answers "which files, and
// which keys" and nothing else — no network, no CLI flags, no exit codes.
//
// It lives in this package rather than beside it because the write path is
// package-private on purpose: BackupAndWrite takes a `guard`, resolveGuard is
// unexported, and writeExclusiveBackup takes a `rootFS`. A package outside
// cannot build any of the three, and widening them would put the atomicity and
// containment invariants in reach of code that has no reason to hold them.

// Options is what the user asked for: a gateway, a credential, and a model.
type Options struct {
	// Home overrides the home directory the paths are resolved against. Empty
	// means os.UserHomeDir. Tests set it to t.TempDir(); §9 of the schema
	// matrix forbids reading the developer's real config, and this is the seam
	// that makes that possible.
	Home string
	// BaseURL is the gateway as the user typed it. The adapter decides whether
	// it gains /v1; callers must not pre-normalise, or cline would lose its one
	// difference from the other eight.
	BaseURL string
	// APIKey is the credential to write. It is never logged and never appears
	// in an error.
	APIKey string
	// Model is the model id the client should default to.
	Model string
	// Tiers carries per-tier model overrides for claude (fable, opus, sonnet,
	// haiku). A tier absent from this map has no value from this source.
	Tiers map[string]string
	// ServerTiers carries the same overrides from the server's config, the
	// lower-precedence source. A tier present in neither map keeps whatever key
	// the user already had — see claude.go.
	ServerTiers map[string]string
}

// Result reports what a write actually did.
type Result struct {
	// Backups lists the .bak files created, as absolute paths, in the order the
	// writes happened. Empty for a client that created its file fresh.
	Backups []string
	// Changed is false when every file was already in the target state, which
	// is a success: a second connect must not rewrite anything.
	Changed bool
}

// Adapter is one client. Apply and Reset are idempotent, and both return
// ErrAlreadyPatched as a nil-error-free success via Changed.
type Adapter interface {
	// ID is the value `connect --tools` accepts.
	ID() string
	// Paths resolves the client's files against opts.Home.
	Paths(opts Options) (Paths, error)
	// Plan reports the edits Apply would make without touching the disk, so
	// --dry-run has a path that cannot write even if it tried.
	Plan(opts Options) ([]FilePlan, error)
	// Apply writes the edits.
	Apply(opts Options) (Result, error)
	// Reset removes what Apply wrote.
	Reset(opts Options) (Result, error)
}

// FilePlan is one file's worth of edits, resolved but not yet applied.
type FilePlan struct {
	File TargetFile
	// Edits are applied in order. A later edit sees the earlier ones, which is
	// what lets a TOML table be created key by key.
	Edits []Edit
	// RemoveFile asks the writer to delete the file instead of writing it. Only
	// omp uses it, and only when what would be left is a bare `providers:`
	// header (matrix §3.9).
	RemoveFile bool
}

// baseAdapter holds the helpers every client needs, so the nine files are key
// tables rather than plumbing.
type baseAdapter struct {
	id    string
	paths func(home string) Paths
}

func (b baseAdapter) ID() string { return b.id }

// Paths resolves the client's files for this run.
func (b baseAdapter) Paths(opts Options) (Paths, error) {
	home := opts.Home
	if home == "" {
		h, err := homeDir()
		if err != nil {
			return Paths{}, err
		}
		home = h
	}
	return b.paths(home), nil
}

// applyPlans runs a plan list through the shared write path: resolve, read,
// patch, back up, replace atomically. A plan with no net change is not written.
//
// The read happens through the guard's root rather than os.ReadFile so the
// path that was checked is the path that is read. A missing file is an empty
// document, not an error (matrix §5.2).
func applyPlans(plans []FilePlan, now func() time.Time) (Result, error) {
	var res Result
	for _, p := range plans {
		changed, backup, err := applyOnePlan(p, now)
		if err != nil {
			// A client with two files can fail on the second after the first
			// has changed. Saying so is required (matrix §5.2); rolling back
			// from the backup is a second write with its own failure mode, so
			// this reports and stops.
			if len(res.Backups) > 0 {
				return res, fmt.Errorf("%s after %d file(s) changed (backups: %v): %w",
					p.File.Purpose, len(res.Backups), res.Backups, err)
			}
			return res, err
		}
		if backup != "" {
			res.Backups = append(res.Backups, backup)
		}
		res.Changed = res.Changed || changed
	}
	return res, nil
}

// applyOnePlan writes one file. It returns whether anything changed and the
// backup path, if one was made.
func applyOnePlan(p FilePlan, now func() time.Time) (changed bool, backup string, err error) {
	g, err := resolveGuard(p.File.Dir, relFromDir(p.File.Dir, p.File.Path))
	if err != nil {
		return false, "", fmt.Errorf("%s: %w", p.File.Purpose, err)
	}
	old, err := readThroughGuard(g)
	if err != nil {
		return false, "", fmt.Errorf("%s: %w", p.File.Purpose, err)
	}
	if p.RemoveFile {
		return removeThroughGuard(g, old != nil)
	}
	var cur = old
	for _, e := range p.Edits {
		next, patchErr := Patch(cur, p.File.Format, []Edit{e})
		if errors.Is(patchErr, ErrAlreadyPatched) {
			continue
		}
		if patchErr != nil {
			return false, "", fmt.Errorf("%s: %w", p.File.Purpose, patchErr)
		}
		cur = next
	}
	if len(cur) == 0 || string(cur) == string(old) {
		return false, "", nil
	}
	wr, err := BackupAndWrite(g, old, cur, now, hooks{})
	if err != nil {
		return false, "", fmt.Errorf("%s: %w", p.File.Purpose, err)
	}
	if wr.Backup != "" {
		backup = wr.Backup
	}
	return true, backup, nil
}

// readThroughGuard reads the target through the same root the write uses.
// A file that does not exist yet is an empty document, which is what lets a
// first-ever connect create it without special-casing.
func readThroughGuard(g guard) ([]byte, error) {
	if !g.existed {
		return nil, nil
	}
	r, err := os.OpenRoot(g.baseResolved)
	if err != nil {
		return nil, fmt.Errorf("%w: open root: %w", ErrPathEscape, err)
	}
	defer func() { _ = r.Close() }()
	b, err := r.ReadFile(g.rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", g.rel, err)
	}
	return b, nil
}

// removeThroughGuard deletes a file the adapter decided should not exist.
func removeThroughGuard(g guard, existed bool) (bool, string, error) {
	if !existed {
		return false, "", nil
	}
	r, err := os.OpenRoot(g.baseResolved)
	if err != nil {
		return false, "", fmt.Errorf("%w: open root: %w", ErrPathEscape, err)
	}
	defer func() { _ = r.Close() }()
	if err := r.Remove(g.rel); err != nil {
		return false, "", fmt.Errorf("remove %s: %w", g.rel, err)
	}
	return true, "", nil
}

// relFromDir turns an absolute target into the relative path a guard takes.
// The base is always an ancestor by construction; a target outside it is a
// programming error, and resolveGuard refuses it downstream rather than silently
// reaching up the tree.
func relFromDir(dir, path string) string {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return path
	}
	return rel
}
