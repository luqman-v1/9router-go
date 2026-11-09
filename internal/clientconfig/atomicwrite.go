package clientconfig

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/log"
)

// hooks collects the I/O seams a test needs to fail mid-operation.
//
// They live in a struct passed down the call, never in package variables: a
// package-level seam races with t.Parallel() and leaks a stub into the next
// test in the same package.
type hooks struct {
	write  func(f *os.File, p []byte) error
	sync   func(f *os.File) error
	rename func(r rootFS, oldRel, newRel string) error
	// openRoot opens the base directory for one operation. It is the seam a
	// dry-run uses to put a counting root in the write path (§5.2): without it
	// the counter is a wrapper nobody can install.
	openRoot func(baseResolved string) (rootFS, error)
}

var defaultHooks = hooks{
	write:  func(f *os.File, p []byte) error { _, err := f.Write(p); return err },
	sync:   func(f *os.File) error { return f.Sync() },
	rename: func(r rootFS, oldRel, newRel string) error { return r.Rename(oldRel, newRel) },
	openRoot: func(baseResolved string) (rootFS, error) {
		r, err := os.OpenRoot(baseResolved)
		if err != nil {
			return nil, err
		}
		return osRootFS{root: r}, nil
	},
}

func (h hooks) withDefaults() hooks {
	if h.write == nil {
		h.write = defaultHooks.write
	}
	if h.sync == nil {
		h.sync = defaultHooks.sync
	}
	if h.rename == nil {
		h.rename = defaultHooks.rename
	}
	if h.openRoot == nil {
		h.openRoot = defaultHooks.openRoot
	}
	return h
}

// WriteFileAtomic replaces the target with content through a temp file in the
// same directory, renaming it into place only after it is fully written,
// synced, chmodded and closed.
//
// Every failure before the rename leaves the original byte-for-byte intact: the
// temp file absorbs a full disk, not the target. A failed rename returns an
// error — there is deliberately no fallback to writing the target in place,
// because that fallback is exactly the torn-file bug this function exists to
// avoid.
//
// Rename is atomic in the namespace on local ext4/APFS/NTFS, but not across
// NFS/SMB/FAT or through a filter driver, and namespace atomicity is not
// durability. Do not describe this as "atomic on all filesystems"; see the
// caveat table in the spec §5.6.
func WriteFileAtomic(g guard, content []byte, h hooks) error {
	h = h.withDefaults()

	r, err := h.openRoot(g.baseResolved)
	if err != nil {
		return fmt.Errorf("%w: open root %s: %w", ErrPathEscape, g.baseResolved, err)
	}
	defer func() { _ = r.Close() }()

	if len(content) > LargeFileWarningBytes {
		// A warning, not a refusal: the file is the user's, and D7 keeps the
		// decision with them. The number is worth saying out loud because the
		// parsers build a tree on top of the whole file — 121x its size for
		// TOML, measured — so a "large" config is where that becomes memory
		// rather than latency.
		log.Warn("clientconfig", "writing a large config file",
			"path", g.rel, "bytes", len(content), "threshold", LargeFileWarningBytes)
	}

	perm := os.FileMode(constants.FilePermKey)
	if g.existed {
		// Preserve the mode the user already chose; a config that was 0640 for
		// a group-readable setup should not become private behind their back.
		// Stat through Root, not on an absolute path.
		if fi, statErr := r.Stat(g.rel); statErr == nil {
			perm = fi.Mode().Perm()
		}
	}

	tempFile, tempRel, err := createTemp(r, g.rel)
	if err != nil {
		return err
	}
	// Runs on every path from here on, including success: after a successful
	// rename the name no longer exists, and Remove reports fs.ErrNotExist.
	defer func() {
		if removeErr := r.Remove(tempRel); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			// A temp file the antivirus is holding is rare but real, and it is
			// the user's disk. Name it so they can clean up.
			log.Warn("clientconfig", "could not remove temp file", "path", tempRel, "error", removeErr)
		}
	}()

	if err := writeTemp(tempFile, tempRel, content, perm, h); err != nil {
		return err
	}
	if err := renameWithRetry(r, tempRel, g.rel, h); err != nil {
		return fmt.Errorf("%w: rename %s → %s: %w", ErrWriteFailed, tempRel, g.rel, err)
	}
	// Best effort, POSIX only. On Windows this is a no-op that reports
	// unsupported, which must not fail the write.
	fsyncDir(r, filepath.Dir(g.rel))
	return nil
}

// writeTemp fills the temp file and closes it, so the rename always moves a
// file whose bytes are on disk rather than in a page cache.
//
// The handle is the one createTemp opened, never a fresh open by name. A
// reopen addresses the name again, and a name in a directory someone else can
// write is a name someone else can replace with a symlink between the create
// and the second open — so the bytes would land wherever the link pointed. It
// also reopened without O_TRUNC, which trusts that the file is still empty.
// Keeping the descriptor removes the window and the trust together: the file
// written and the file renamed are the same file object.
func writeTemp(f *os.File, tempRel string, content []byte, perm os.FileMode, h hooks) error {
	if err := h.write(f, content); err != nil {
		_ = f.Close()
		return fmt.Errorf("%w: write temp %s: %w", ErrWriteFailed, tempRel, err)
	}
	if err := h.sync(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("%w: sync temp %s: %w", ErrWriteFailed, tempRel, err)
	}
	// Chmod on the handle after writing: umask already masked the perm at
	// creation, and doing it here keeps the file writable while it is written.
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return fmt.Errorf("%w: chmod temp %s: %w", ErrWriteFailed, tempRel, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("%w: close temp %s: %w", ErrWriteFailed, tempRel, err)
	}
	return nil
}

// createTemp makes an exclusively-created temp file next to the target.
//
// It must be in the target's directory, not os.TempDir: rename is atomic only
// within one filesystem, and a cross-device rename either fails with EXDEV or
// silently degrades to copy+unlink, which breaks the invariant. os.Root has no
// CreateTemp, so the name is drawn here.
//
// The temp is chmodded owner-writable on the handle before it is closed, rather
// than being created with the target's perm. OpenFile's perm argument is masked
// by umask and that mask can include the owner write bit — under umask 0277 a
// 0640 target creates the temp 0400 — so writeTemp's reopen for the write gets
// EACCES and the write fails before it starts. Chmod on the handle is not
// subject to umask. writeTemp applies the target's real mode once the bytes are
// down, which is where the user's choice is honoured.
func createTemp(r rootFS, relTarget string) (*os.File, string, error) {
	dir := filepath.Dir(relTarget)
	for i := 0; i < 16; i++ {
		nonce := make([]byte, 8)
		if _, err := rand.Read(nonce); err != nil {
			return nil, "", fmt.Errorf("%w: nonce: %w", ErrWriteFailed, err)
		}
		name := filepath.Join(dir, fmt.Sprintf(".tmp-%x", nonce))
		f, err := r.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(constants.FilePermKey))
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("%w: create temp %s: %w", ErrWriteFailed, name, err)
		}
		// Restored on the handle because the create perm was umask-masked. The
		// temp holds the key's new content, so it stays owner-only until the
		// rename.
		if err := f.Chmod(os.FileMode(constants.FilePermKey)); err != nil {
			_ = f.Close()
			_ = r.Remove(name)
			return nil, "", fmt.Errorf("%w: chmod temp %s: %w", ErrWriteFailed, name, err)
		}
		// The handle goes back to the caller, which writes through it. Not
		// closed here: a close-then-reopen-by-name is the swap window
		// writeTemp's comment describes.
		return f, name, nil
	}
	return nil, "", fmt.Errorf("%w: temp name exhausted in %s", ErrWriteFailed, dir)
}

// defaultNow is the production clock. Tests inject a fixed one through the
// now parameter of writeExclusiveBackup and BackupAndWrite.
func defaultNow() time.Time { return time.Now() }
