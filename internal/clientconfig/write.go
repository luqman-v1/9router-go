package clientconfig

import (
	"fmt"
	"time"
)

// WriteResult reports what one write did, so the caller can show the user the
// backup it can restore from.
type WriteResult struct {
	Backup string // relative name of the .bak file, empty if none was made
}

// BackupAndWrite is the whole pipeline from the spec §5.2: back up the previous
// content, then replace the file atomically.
//
// content must already be the validated new body. The backup is written from
// oldContent rather than by re-reading the target: that closes the gap between
// "read to validate" and "read to back up", and guarantees a backup is never
// taken from a file that changed after it was parsed.
//
// A guard for a file that does not exist yet skips the backup — there is
// nothing to lose — and creates it owner-only.
func BackupAndWrite(g guard, oldContent, content []byte, now func() time.Time, h hooks) (WriteResult, error) {
	if now == nil {
		now = defaultNow
	}
	h = h.withDefaults()
	r, err := h.openRoot(g.baseResolved)
	if err != nil {
		return WriteResult{}, fmt.Errorf("%w: open root %s: %w", ErrPathEscape, g.baseResolved, err)
	}
	defer func() { _ = r.Close() }()

	var res WriteResult
	if g.existed {
		res.Backup, err = writeExclusiveBackup(r, g.rel, oldContent, now)
		if err != nil {
			return WriteResult{}, err
		}
	}
	if err := WriteFileAtomic(g, content, h); err != nil {
		// The backup stays on disk on purpose: it is the state the user had
		// before a write that failed, and it is the only copy if the target was
		// damaged by something outside this process.
		return res, fmt.Errorf("write %s: %w", g.rel, err)
	}
	return res, nil
}
