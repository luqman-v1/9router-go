//go:build !windows

package clientconfig

import (
	"errors"
	"syscall"

	"9router/proxy/internal/log"
)

// fsyncDir flushes the directory entry the rename just changed, so a power
// loss after a successful write cannot lose the name.
//
// Best effort by design: a platform that reports ENOTSUP/EINVAL for a directory
// sync has nothing more to give, and failing the write over it would turn a
// working save into an error. A real I/O error is different — that is a dying
// disk, and the user needs to see it.
func fsyncDir(r rootFS, dir string) {
	f, err := r.Open(dir)
	if err != nil {
		log.Warn("clientconfig", "could not open directory for sync", "dir", dir, "error", err)
		return
	}
	defer func() { _ = f.Close() }()
	if err := f.Sync(); err != nil {
		if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) ||
			errors.Is(err, syscall.EBADF) || errors.Is(err, syscall.EPERM) {
			return // this filesystem cannot sync a directory; nothing to do
		}
		log.Warn("clientconfig", "directory sync failed", "dir", dir, "error", err)
	}
}
