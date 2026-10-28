package clientconfig

import (
	"os"
	"time"
)

// rootFS is the filesystem seam the write path runs on.
//
// It exists because a dry-run has to *measure* the writes it forbids, and a
// measurement that cannot sit in the write path measures nothing. The write
// path took a concrete *os.Root, so there was nowhere to put a counter: the
// wrapper was a dead end, and the "0 disk writes" guarantee of §5.1 had no
// enforcement mechanism.
//
// The interface is deliberately the *mutating* surface only. Reads are not
// counted, and every method here changes the filesystem — so an implementation
// cannot forget to count a write by forgetting to implement a method.
//
// *os.Root satisfies it unchanged; countingRoot wraps it and counts.
type rootFS interface {
	// OpenFile opens a file. O_WRONLY/O_RDWR/O_CREATE/O_APPEND/O_TRUNC count as
	// writes.
	OpenFile(name string, flag int, perm os.FileMode) (*os.File, error)
	// Create creates or truncates a file.
	Create(name string) (*os.File, error)
	// Mkdir creates a directory.
	Mkdir(name string, perm os.FileMode) error
	// MkdirAll creates a directory and any missing parents.
	MkdirAll(path string, perm os.FileMode) error
	// Remove removes a file or empty directory.
	Remove(name string) error
	// RemoveAll removes a path and any children.
	RemoveAll(path string) error
	// WriteFile writes data to a file.
	WriteFile(name string, data []byte, perm os.FileMode) error
	// Rename moves a name within the root.
	Rename(oldName, newName string) error
	// Chmod changes a file's mode.
	Chmod(name string, mode os.FileMode) error
	// Chown changes a file's owner.
	Chown(name string, uid, gid int) error
	// Chtimes changes a file's access and modification times.
	Chtimes(name string, atime time.Time, mtime time.Time) error
	// Symlink creates a symbolic link.
	Symlink(oldName, newName string) error
	// Link creates a hard link.
	Link(oldName, newName string) error
	// Open opens a file for reading. It is here because the write path reads
	// the target's mode before overwriting it; reads are not counted.
	Open(name string) (*os.File, error)
	// Stat reports a file's metadata. Reads are not counted.
	Stat(name string) (os.FileInfo, error)
	// Close releases the root.
	Close() error
}

// osRootFS adapts *os.Root to rootFS. It is a thin pass-through: the wrapper
// adds no behaviour of its own.
type osRootFS struct {
	root *os.Root
}

func (o osRootFS) OpenFile(name string, flag int, perm os.FileMode) (*os.File, error) {
	return o.root.OpenFile(name, flag, perm)
}
func (o osRootFS) Create(name string) (*os.File, error) { return o.root.Create(name) }
func (o osRootFS) Mkdir(name string, perm os.FileMode) error {
	return o.root.Mkdir(name, perm)
}
func (o osRootFS) MkdirAll(path string, perm os.FileMode) error {
	return o.root.MkdirAll(path, perm)
}
func (o osRootFS) Remove(name string) error { return o.root.Remove(name) }
func (o osRootFS) RemoveAll(path string) error {
	return o.root.RemoveAll(path)
}
func (o osRootFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	return o.root.WriteFile(name, data, perm)
}
func (o osRootFS) Rename(oldName, newName string) error { return o.root.Rename(oldName, newName) }
func (o osRootFS) Chmod(name string, mode os.FileMode) error {
	return o.root.Chmod(name, mode)
}
func (o osRootFS) Chown(name string, uid, gid int) error { return o.root.Chown(name, uid, gid) }
func (o osRootFS) Chtimes(name string, atime time.Time, mtime time.Time) error {
	return o.root.Chtimes(name, atime, mtime)
}
func (o osRootFS) Symlink(oldName, newName string) error {
	return o.root.Symlink(oldName, newName)
}
func (o osRootFS) Link(oldName, newName string) error    { return o.root.Link(oldName, newName) }
func (o osRootFS) Open(name string) (*os.File, error)    { return o.root.Open(name) }
func (o osRootFS) Stat(name string) (os.FileInfo, error) { return o.root.Stat(name) }
func (o osRootFS) Close() error                          { return o.root.Close() }
