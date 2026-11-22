package service

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type hostStub struct {
	goos        string
	euid        int
	nonElevated bool
	bins        map[string]string
	dirs        map[string]bool
}

// stubHost swaps the OS and privilege seams so every branch runs on one host.
func stubHost(t *testing.T, h hostStub) {
	t.Helper()
	previousGoos, previousEuid := goos, geteuid
	previousLook, previousIsDir := lookPath, isDir
	previousElevated := windowsIsElevated
	previousRead, previousWrite, previousDelete := windowsRunRead, windowsRunWrite, windowsRunDelete
	goos = h.goos
	geteuid = func() int { return h.euid }
	lookPath = func(name string) string { return h.bins[name] }
	isDir = func(path string) bool { return h.dirs[path] }
	windowsIsElevated = func() bool { return !h.nonElevated }
	runEntries := map[string]string{}
	windowsRunRead = func(name string) (string, bool, error) {
		val, ok := runEntries[name]
		return val, ok, nil
	}
	windowsRunWrite = func(name, value string) error {
		runEntries[name] = value
		return nil
	}
	windowsRunDelete = func(name string) (bool, error) {
		if _, ok := runEntries[name]; !ok {
			return false, nil
		}
		delete(runEntries, name)
		return true, nil
	}
	t.Cleanup(func() {
		goos, geteuid, lookPath, isDir = previousGoos, previousEuid, previousLook, previousIsDir
		windowsIsElevated = previousElevated
		windowsRunRead, windowsRunWrite, windowsRunDelete = previousRead, previousWrite, previousDelete
	})
}

func stubWindowsRun(t *testing.T, initial map[string]string) map[string]string {
	t.Helper()
	previousRead, previousWrite, previousDelete := windowsRunRead, windowsRunWrite, windowsRunDelete
	entries := map[string]string{}
	maps.Copy(entries, initial)
	windowsRunRead = func(name string) (string, bool, error) {
		val, ok := entries[name]
		return val, ok, nil
	}
	windowsRunWrite = func(name, value string) error {
		entries[name] = value
		return nil
	}
	windowsRunDelete = func(name string) (bool, error) {
		if _, ok := entries[name]; !ok {
			return false, nil
		}
		delete(entries, name)
		return true, nil
	}
	t.Cleanup(func() {
		windowsRunRead, windowsRunWrite, windowsRunDelete = previousRead, previousWrite, previousDelete
	})
	return entries
}

func stubRunFunc(t *testing.T, fn func(name string, args ...string) (string, error)) *[]svcCall {
	t.Helper()
	previous := run
	calls := &[]svcCall{}
	run = func(name string, args ...string) (string, error) {
		*calls = append(*calls, svcCall{name: name, args: slices.Clone(args)})
		if fn == nil {
			return "", nil
		}
		return fn(name, args...)
	}
	t.Cleanup(func() { run = previous })
	return calls
}

// redirectInitPaths points the root-owned OpenRC and runit paths at a temp tree.
func redirectInitPaths(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	previousOpenRC := openRCServicePath
	previousRunitDir, previousRunitLink := runitSystemDir, runitSystemLink
	openRCServicePath = filepath.Join(root, "etc", "init.d", "9router-go")
	runitSystemDir = filepath.Join(root, "etc", "sv", "9router-go")
	runitSystemLink = filepath.Join(root, "var", "service", "9router-go")
	t.Cleanup(func() {
		openRCServicePath = previousOpenRC
		runitSystemDir, runitSystemLink = previousRunitDir, previousRunitLink
	})
	return root
}

// stubCrontab swaps the crontab reader/writer for an in-memory one and returns
// the last content written.
func stubCrontab(t *testing.T, initial string) *string {
	t.Helper()
	previousRead, previousWrite := readCrontab, writeCrontab
	content := initial
	written := &content
	readCrontab = func() string { return *written }
	writeCrontab = func(next string) error {
		*written = next
		return nil
	}
	t.Cleanup(func() { readCrontab, writeCrontab = previousRead, previousWrite })
	return written
}

// setHome points the user-scoped paths at a temp home. It swaps the userHomeDir
// seam rather than HOME because os.UserHomeDir reads %USERPROFILE% on Windows.
func setHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	stubUserHomeDir(t, home, nil)
	return home
}

func stubNoHome(t *testing.T) {
	t.Helper()
	stubUserHomeDir(t, "", errors.New("no home directory"))
}

func stubUserHomeDir(t *testing.T, home string, err error) {
	t.Helper()
	previous := userHomeDir
	userHomeDir = func() (string, error) { return home, err }
	t.Cleanup(func() { userHomeDir = previous })
}

func stubWindowsTaskUser(t *testing.T, user string) {
	t.Helper()
	previous := windowsTaskUser
	windowsTaskUser = func() string { return user }
	t.Cleanup(func() { windowsTaskUser = previous })
}

// linkStub records runit symlink/remove calls, still performing them where the
// runner allows it; on Windows the symlink needs a privilege, so it is only recorded.
type linkStub struct {
	created []string
	removed []string
	links   bool
}

func stubLinks(t *testing.T) *linkStub {
	t.Helper()
	previousSymlink, previousRemove := symlink, remove
	stub := &linkStub{links: probeSymlink(t)}
	symlink = func(oldname, newname string) error {
		stub.created = append(stub.created, newname)
		if !stub.links {
			return nil
		}
		return previousSymlink(oldname, newname)
	}
	remove = func(path string) error {
		stub.removed = append(stub.removed, path)
		return previousRemove(path)
	}
	t.Cleanup(func() { symlink, remove = previousSymlink, previousRemove })
	return stub
}

// probeSymlink tries a real symlink in a temp dir: the privilege varies by OS,
// developer mode, and CI image.
func probeSymlink(t *testing.T) bool {
	t.Helper()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return os.Symlink(target, filepath.Join(t.TempDir(), "link")) == nil
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
