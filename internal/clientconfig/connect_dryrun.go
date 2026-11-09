package clientconfig

import (
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

// Dry-run is a claim about two counters, not a promise in a comment: the number
// of network requests and the number of disk-mutating calls must both be zero
// (docs/specs/connect-security.md §5.1). These two wrappers are what a test
// counts.
//
// Both count at the primitive — RoundTrip, and the filesystem calls that can
// change a directory — never at the business-logic layer. A counter above an
// `WriteFileAtomic` would miss a new `MkdirAll` added later and stay green while
// the guarantee broke (§5.2).
//
// The filesystem counter reaches the write path through the `openRoot` hook:
// the write path opens its root through that seam, so a counting root can be
// installed there. A wrapper that cannot be installed measures nothing.

// countingTransport counts every RoundTrip, whether or not it succeeds. A
// request that fails DNS or is refused at connect time still *left* the process;
// the contract says zero attempts, not zero successful responses.
type countingTransport struct {
	inner http.RoundTripper
	n     atomic.Int64
}

func (t *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.n.Add(1)
	return t.inner.RoundTrip(r)
}

// Requests reports how many RoundTrip calls were made.
func (t *countingTransport) Requests() int64 { return t.n.Load() }

// countingRoot counts the filesystem operations that can change a file or
// directory. Reads go through uncounted — a dry run is allowed to read the
// config to show what would change.
//
// It implements rootFS, so it can be handed to the write path through the
// `openRoot` hook; that is the difference between a counter and a decoration.
type countingRoot struct {
	inner rootFS
	// n is shared by every wrapper of one operation. One write opens more
	// than one root — BackupAndWrite opens a root for the backup and
	// WriteFileAtomic opens another for the target — so a counter that
	// belonged to the wrapper would either split the tally in two or, worse,
	// make the wrapper reuse itself and drop the first root on the floor: the
	// wrapper's inner would point at the second root and the first root's
	// descriptor would never be closed.
	n *atomic.Int64
}

// newCountingRoot wraps a root in a counter of its own.
func newCountingRoot(inner rootFS) *countingRoot {
	return &countingRoot{inner: inner, n: &atomic.Int64{}}
}

// newCounter returns a counting root with no root behind it: it is the tally
// the openRoot hook shares with the wrappers it hands out.
func newCounter() *countingRoot {
	return newCountingRoot(nil)
}

// Writes reports how many mutating calls were made.
func (r *countingRoot) Writes() int64 { return r.n.Load() }

// count records one mutation.
func (r *countingRoot) count() { r.n.Add(1) }

// OpenFile counts only when the flags allow a change. O_RDONLY alone is a read.
func (r *countingRoot) OpenFile(name string, flag int, perm os.FileMode) (*os.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_APPEND|os.O_TRUNC) != 0 {
		r.count()
	}
	return r.inner.OpenFile(name, flag, perm)
}

// Create creates or truncates a file: a change.
func (r *countingRoot) Create(name string) (*os.File, error) {
	r.count()
	return r.inner.Create(name)
}

// Mkdir creates a directory: a change.
func (r *countingRoot) Mkdir(name string, perm os.FileMode) error {
	r.count()
	return r.inner.Mkdir(name, perm)
}

// MkdirAll creates a directory tree: a change. This is the exact case the
// spec names — a client config directory created for the first time.
func (r *countingRoot) MkdirAll(path string, perm os.FileMode) error {
	r.count()
	return r.inner.MkdirAll(path, perm)
}

// Remove deletes a name.
func (r *countingRoot) Remove(name string) error {
	r.count()
	return r.inner.Remove(name)
}

// RemoveAll deletes a tree: a change.
func (r *countingRoot) RemoveAll(path string) error {
	r.count()
	return r.inner.RemoveAll(path)
}

// WriteFile writes content: a change.
func (r *countingRoot) WriteFile(name string, data []byte, perm os.FileMode) error {
	r.count()
	return r.inner.WriteFile(name, data, perm)
}

// Rename creates a new name for an existing file: a change, even though no byte
// is written.
func (r *countingRoot) Rename(oldName, newName string) error {
	r.count()
	return r.inner.Rename(oldName, newName)
}

// Chmod changes a file's mode: a change.
func (r *countingRoot) Chmod(name string, mode os.FileMode) error {
	r.count()
	return r.inner.Chmod(name, mode)
}

// Chown changes a file's owner: a change.
func (r *countingRoot) Chown(name string, uid, gid int) error {
	r.count()
	return r.inner.Chown(name, uid, gid)
}

// Chtimes changes a file's timestamps: a change.
func (r *countingRoot) Chtimes(name string, atime time.Time, mtime time.Time) error {
	r.count()
	return r.inner.Chtimes(name, atime, mtime)
}

// Symlink creates a symbolic link: a change.
func (r *countingRoot) Symlink(oldName, newName string) error {
	r.count()
	return r.inner.Symlink(oldName, newName)
}

// Link creates a hard link: a change.
func (r *countingRoot) Link(oldName, newName string) error {
	r.count()
	return r.inner.Link(oldName, newName)
}

// Open reads a file. Reads are not counted.
func (r *countingRoot) Open(name string) (*os.File, error) { return r.inner.Open(name) }

// Stat reads metadata. Reads are not counted.
func (r *countingRoot) Stat(name string) (os.FileInfo, error) { return r.inner.Stat(name) }

// Close releases the underlying root.
func (r *countingRoot) Close() error { return r.inner.Close() }

// countingRootHook returns a hook that opens the base directory through a
// counting root, so the write path's own mutations are counted.
//
// The counter lives on the countingRoot, not on the hook, because the hook is
// called once per operation while the counter must survive all of them. Each
// openRoot call gets its own wrapper — the roots are closed independently, and
// a shared wrapper would close the first root twice and never close the
// second.
func countingRootHook(counter *countingRoot) hooks {
	return hooks{
		openRoot: func(baseResolved string) (rootFS, error) {
			inner, err := defaultHooks.openRoot(baseResolved)
			if err != nil {
				return nil, err
			}
			return &countingRoot{inner: inner, n: counter.n}, nil
		},
	}
}
