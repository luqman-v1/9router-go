package clientconfig

import (
	"fmt"
	"time"
)

// renameBackoff is the Windows lock retry budget: 10+25+50+100+200 ≈ 385ms
// over five attempts. Long enough for an antivirus scan to release the target,
// short enough that a genuinely locked file still fails while the user waits.
var renameBackoff = []time.Duration{
	10 * time.Millisecond,
	25 * time.Millisecond,
	50 * time.Millisecond,
	100 * time.Millisecond,
	200 * time.Millisecond,
}

// renameWithRetry moves the temp file onto the target, retrying only the
// transient lock codes the platform recognises.
//
// It never falls back to truncating the target. If the rename cannot happen,
// the write did not happen, and saying so is the whole point — a silent
// fallback to O_TRUNC is the torn-file bug wearing an error-handling costume.
func renameWithRetry(r rootFS, oldRel, newRel string, h hooks) error {
	err := h.rename(r, oldRel, newRel)
	if err == nil || !isRetryableRenameErr(err) {
		return err
	}
	for _, wait := range renameBackoff {
		time.Sleep(wait)
		if err = h.rename(r, oldRel, newRel); err == nil {
			return nil
		}
		if !isRetryableRenameErr(err) {
			return err
		}
	}
	// Only reachable on a platform whose isRetryableRenameErr can be true.
	return fmt.Errorf("file is locked by another program; close it and retry: %w", err)
}
