// Package clientconfig writes client CLI configuration files without handing
// a path-traversal or a half-written file to the user.
//
// The work splits in two layers on purpose. resolveGuard inspects the path and
// produces a good typed error; it is a check, and like every check-by-path it
// has a window between the check and the open. os.Root is the thing that
// actually enforces containment, at the syscall, for every component. Neither
// layer replaces the other: os.Root does not tell the user why, and the
// pre-flight cannot stop a swap. See docs/specs/safe-file-operations.md §3.3.
package clientconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var (
	// ErrPathEscape reports a path that leaves the base directory, or one that
	// cannot be classified (absolute, has a volume name, an ADS colon, a device
	// name, a ".." component) before any filesystem access happens.
	ErrPathEscape = errors.New("clientconfig: path escapes base directory")

	// ErrSymlinkAncestor reports a symlink or Windows junction, on any
	// component, that resolves outside the base directory — or on the final
	// component even when it stays inside. Renaming onto a symlink replaces the
	// link, not its target, which is not what a user writing a config means.
	ErrSymlinkAncestor = errors.New("clientconfig: symlink ancestor")

	// ErrNotRegular reports an existing target that is not a regular file.
	ErrNotRegular = errors.New("clientconfig: target is not a regular file")

	// ErrBaseMissing reports a base directory that does not exist. Creating it
	// is the caller's decision, made after its own ancestor check, so this is
	// distinct from ErrPathEscape.
	ErrBaseMissing = errors.New("clientconfig: base directory does not exist")

	// ErrBackupCollision reports that every candidate backup name already
	// exists. Nothing is overwritten; the caller retries later or reports it.
	ErrBackupCollision = errors.New("clientconfig: backup name collision")

	// ErrWriteFailed reports a failed create, write, sync, close, or rename.
	// The original file is untouched: everything before the rename writes to a
	// temp file, and a failed rename is all-or-nothing.
	ErrWriteFailed = errors.New("clientconfig: write failed")
)

// guard is a verified path. Mutating operations use rel, never an absolute
// path, so containment is enforced by os.Root rather than by string prefixes.
type guard struct {
	baseResolved string // EvalSymlinks(baseAbs) — the single comparison anchor
	rel          string // relative path with native separators, containment checked
	existed      bool   // the target existed before this operation
}

// reservedNames are the DOS device names Windows refuses or redirects,
// matched case-insensitively and with or without an extension.
var reservedNames = []string{"CON", "PRN", "AUX", "NUL",
	"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
	"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9"}

// validateRelComponents rejects the spellings that make a "relative" path
// something else. It runs before any filesystem access, so a rejected path
// never causes a syscall.
func validateRelComponents(relPath string) error {
	if relPath == "" || filepath.IsAbs(relPath) || filepath.VolumeName(relPath) != "" {
		return fmt.Errorf("%w: %q", ErrPathEscape, relPath)
	}
	for _, comp := range splitPathComponents(relPath) {
		if comp == "" || comp == "." || comp == ".." {
			return fmt.Errorf("%w: bad component %q in %q", ErrPathEscape, comp, relPath)
		}
		// Refused on every platform, not just Windows: a client config path is
		// never legitimately an alternate data stream, and a config written on
		// one OS should not become a colon file that breaks on the other.
		// ponytail: no per-OS split; if a POSIX client ever needs ":" in a
		// config filename, gate this check on runtime.GOOS.
		if strings.Contains(comp, ":") {
			return fmt.Errorf("%w: colon in component %q", ErrPathEscape, comp)
		}
		if isReservedName(comp) {
			return fmt.Errorf("%w: reserved device name %q", ErrPathEscape, comp)
		}
	}
	return nil
}

// splitPathComponents splits a relative path on both separator spellings.
//
// A path written on Windows uses "\", a path written on POSIX uses "/". A
// client config path is portable — the same file is addressed the same way
// whichever OS wrote it — so both spellings are read as separators on every
// platform. Without the split, "..\..\etc\passwd" is a single harmless-looking
// file name on POSIX, and "a\NUL" is a file name that hides a reserved device
// name inside it. Refusing them is the same policy the colon check already
// follows: one path, one meaning, whichever OS reads it.
func splitPathComponents(relPath string) []string {
	parts := make([]string, 0, 4)
	start := 0
	for i := 0; i < len(relPath); i++ {
		if relPath[i] == '/' || relPath[i] == '\\' {
			parts = append(parts, relPath[start:i])
			start = i + 1
		}
	}
	return append(parts, relPath[start:])
}

func isReservedName(comp string) bool {
	stem := comp
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	for _, name := range reservedNames {
		if strings.EqualFold(stem, name) {
			return true
		}
	}
	return false
}

// resolveGuard verifies baseDir/relPath and returns a guard. It does not
// create anything: a missing base is ErrBaseMissing, not a silent MkdirAll.
func resolveGuard(baseDir, relPath string) (guard, error) {
	if err := validateRelComponents(relPath); err != nil {
		return guard{}, err
	}

	baseAbs, err := filepath.Abs(baseDir)
	if err != nil {
		return guard{}, fmt.Errorf("%w: abs base: %w", ErrPathEscape, err)
	}
	baseResolved, err := filepath.EvalSymlinks(baseAbs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return guard{}, fmt.Errorf("%w: %s", ErrBaseMissing, baseAbs)
		}
		return guard{}, fmt.Errorf("%w: eval base: %w", ErrPathEscape, err)
	}

	// Walk up to the nearest ancestor that exists, so a target that is being
	// created for the first time is still checked component by component.
	candidate := filepath.Join(baseResolved, filepath.FromSlash(relPath))
	var remaining []string
	cur := candidate
	var resolved string
	for {
		eval, evalErr := filepath.EvalSymlinks(cur)
		if evalErr == nil {
			resolved = eval
			break
		}
		if !errors.Is(evalErr, fs.ErrNotExist) {
			return guard{}, fmt.Errorf("%w: eval %s: %w", ErrPathEscape, cur, evalErr)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return guard{}, fmt.Errorf("%w: %s", ErrPathEscape, candidate)
		}
		remaining = append([]string{filepath.Base(cur)}, remaining...)
		cur = parent
	}
	resolved = filepath.Join(append([]string{resolved}, remaining...)...)

	// EvalSymlinks above follows real symlinks but is blind to Windows
	// junctions, so it cannot be the only net. Walk every component again with
	// Lstat and refuse an escaping link here and now — deferring the verdict to
	// the lexical check below would accept it, because an alias can sit
	// lexically inside the base while pointing out of it.
	walk := baseResolved
	for _, comp := range splitPathComponents(filepath.Clean(relPath)) {
		walk = filepath.Join(walk, comp)
		fi, statErr := os.Lstat(walk)
		if errors.Is(statErr, fs.ErrNotExist) {
			break
		}
		if statErr != nil {
			return guard{}, fmt.Errorf("%w: lstat %s: %w", ErrPathEscape, walk, statErr)
		}
		if fi.Mode()&os.ModeSymlink != 0 || isNameSurrogateReparse(walk) {
			if escapesResolved(walk, baseResolved) {
				return guard{}, fmt.Errorf("%w: %s", ErrSymlinkAncestor, walk)
			}
			// A link that stays inside is fine on POSIX. On Windows os.Root
			// still refuses a junction here (its target is absolute); see the
			// known-limits table in the spec.
		}
	}

	// Containment by filepath.Rel, never by string prefix: Rel is case-folded
	// on Windows and exact on Unix, and it handles the sibling-name trap
	// ("/a/bc" shares the prefix "/a/b" but is not inside it).
	relCheck, err := filepath.Rel(baseResolved, resolved)
	if err != nil || relCheck == "." || filepath.IsAbs(relCheck) ||
		relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return guard{}, fmt.Errorf("%w: %s", ErrPathEscape, resolved)
	}

	g := guard{baseResolved: baseResolved, rel: filepath.Clean(relCheck)}

	// The final component is inspected on the candidate path, not on the
	// resolved one. EvalSymlinks has already followed the link by the time we
	// get here, so Lstat(resolved) describes the link's target and never the
	// link — a symlink as the last component read as a regular file, and the
	// refusal it is supposed to get never happened. Renaming onto a symlink
	// replaces the link rather than its target, so a link here is refused even
	// when it stays inside the base.
	fi, statErr := os.Lstat(candidate)
	switch {
	case statErr == nil && (fi.Mode()&os.ModeSymlink != 0 || isNameSurrogateReparse(candidate)):
		return guard{}, fmt.Errorf("%w: final component is a link: %s", ErrSymlinkAncestor, candidate)
	case statErr == nil && !fi.Mode().IsRegular():
		return guard{}, fmt.Errorf("%w: %s", ErrNotRegular, candidate)
	case statErr == nil:
		g.existed = true
	case errors.Is(statErr, fs.ErrNotExist):
		// New file — valid, every ancestor was checked above.
	default:
		return guard{}, fmt.Errorf("%w: lstat %s: %w", ErrPathEscape, candidate, statErr)
	}
	return g, nil
}

// escapesResolved reports whether the link at linkPath points outside
// baseResolved.
//
// It must use os.Readlink, never filepath.EvalSymlinks: EvalSymlinks follows a
// link only when ModeSymlink is set, and a Windows junction carries
// ModeIrregular instead, so EvalSymlinks hands back the junction's own name and
// the escape goes unnoticed. os.Readlink handles IO_REPARSE_TAG_MOUNT_POINT
// explicitly. Acceptance case A4b pins this so nobody "optimises" it back.
func escapesResolved(linkPath, baseResolved string) bool {
	tgt, err := os.Readlink(linkPath)
	if err != nil {
		return false // not a link we can read, so nothing to escape through
	}
	if !filepath.IsAbs(tgt) { // POSIX relative symlink
		tgt = filepath.Join(filepath.Dir(linkPath), tgt)
	}
	rel, err := filepath.Rel(baseResolved, filepath.Clean(tgt))
	if err != nil {
		return true // different volume — fail closed
	}
	return rel == ".." || filepath.IsAbs(rel) ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// isNameSurrogateReparse reports whether p is a symlink or a junction.
//
// A successful Readlink is exactly that test: the os package resolves
// IO_REPARSE_TAG_SYMLINK and IO_REPARSE_TAG_MOUNT_POINT and fails for every
// other reparse tag. That matters for Data Deduplication volumes, where
// ordinary files carry tag 0x80000013 — refusing those would break a valid
// server setup, so they must read as false. Plain files and directories fail
// Readlink too, which is also the answer we want.
//
// ponytail: one syscall per component and no tag inspection; add
// GetFileInformationByHandleEx when a caller needs to tell a symlink from a
// junction in the error message.
func isNameSurrogateReparse(p string) bool {
	_, err := os.Readlink(p)
	return err == nil
}
