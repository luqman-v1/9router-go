package clientconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"9router/proxy/internal/constants"
)

const (
	// backupAttempts bounds the collision retry. Each attempt is one O_EXCL
	// open, so eight is not a wait — it is a name search.
	backupAttempts = 8

	// backupPerm keeps a backup, which holds an API key, as private as the
	// file it copied.
	backupPerm os.FileMode = constants.FilePermKey

	// backupStamp is UTC with a literal Z so names sort lexically regardless
	// of the machine's zone, and carries no ":" so Windows accepts them.
	backupStamp = "20060102T150405.000000000Z"
)

// writeExclusiveBackup writes content to a new "<target>.bak.<stamp>" and
// returns its relative name. It never overwrites: a taken name is skipped, so
// two runs in the same nanosecond produce two files.
//
// content comes from the caller's already-parsed buffer, not from re-reading
// the target. That closes the window between "read to parse" and "read to back
// up", and means a backup can never be captured from a file that changed
// underneath the parser.
func writeExclusiveBackup(r rootFS, relTarget string, content []byte, now func() time.Time) (string, error) {
	stamp := now().UTC().Format(backupStamp)
	for i := 0; i < backupAttempts; i++ {
		name := relTarget + ".bak." + stamp
		if i > 0 {
			name = fmt.Sprintf("%s-%d", name, i)
		}
		f, err := r.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, backupPerm)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("%w: create backup %s: %w", ErrWriteFailed, name, err)
		}
		if _, err := f.Write(content); err != nil {
			_ = f.Close()
			_ = r.Remove(name)
			return "", fmt.Errorf("%w: write backup %s: %w", ErrWriteFailed, name, err)
		}
		// Chmod on the handle, not on the path: the perm passed to OpenFile is
		// masked by umask, and under umask 0200 a 0600 backup lands as 0400
		// read-only, which is a file the user cannot clean up later.
		if err := f.Chmod(backupPerm); err != nil {
			_ = f.Close()
			_ = r.Remove(name)
			return "", fmt.Errorf("%w: chmod backup %s: %w", ErrWriteFailed, name, err)
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			_ = r.Remove(name)
			return "", fmt.Errorf("%w: sync backup %s: %w", ErrWriteFailed, name, err)
		}
		if err := f.Close(); err != nil {
			_ = r.Remove(name)
			return "", fmt.Errorf("%w: close backup %s: %w", ErrWriteFailed, name, err)
		}
		return name, nil
	}
	return "", fmt.Errorf("%w: %s after %d attempts", ErrBackupCollision, relTarget, backupAttempts)
}
