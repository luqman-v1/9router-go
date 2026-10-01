package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"9router/proxy/internal/constants"
)

// RunLogMaxBytes is the cap the background log is trimmed to at open time.
// Nothing else ever bounds the file: the daemon holds the descriptor for as
// long as it lives and appends to it every request it logs, so without a cap
// one long-lived install fills the data dir with a log nobody will read.
const RunLogMaxBytes int64 = 16 << 20

// logTailMaxBytes is how much of the tail a LogTail call may read. The lines a
// user asked for are the newest ones, and a busy daemon writes far more between
// two commands than a `logs -n 40` shows, so the file is never worth reading
// whole. A line longer than this is truncated from the left.
const logTailMaxBytes int64 = 256 << 10

// openRunLog opens the background log for appending, trimming it to
// RunLogMaxBytes first. Trimming keeps the newest bytes and drops the oldest,
// so the lines around the failure an operator is looking at survive; the
// leftover head goes to gateway.log.1 and the next start rotates it away.
func openRunLog() (*os.File, error) {
	if err := os.MkdirAll(Dir(), constants.FilePermDir); err != nil {
		return nil, fmt.Errorf("create run dir: %w", err)
	}
	if err := trimIfOversized(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, constants.FilePermFile)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", LogPath(), err)
	}
	return f, nil
}

// trimIfOversized keeps the newest RunLogMaxBytes of the log and moves the
// discarded head aside as gateway.log.1.
//
// Rewriting rather than renaming is the whole point: an operator reads the end
// of this log, and a rotated file would leave the fresh one empty while every
// recent line sat in a generation nothing looks at. The discarded head is
// copied out first so it stays readable, and the file is rewritten with what is
// actually kept rather than truncated to a size that no longer lines up, so the
// newest line always survives.
func trimIfOversized() error {
	size, err := logSize()
	if err != nil {
		return err
	}
	if size <= RunLogMaxBytes {
		return nil
	}
	if err := moveHeadAside(size - RunLogMaxBytes); err != nil {
		return err
	}

	keep, err := readTail(RunLogMaxBytes)
	if err != nil {
		return err
	}
	if err := os.WriteFile(LogPath(), keep, constants.FilePermFile); err != nil {
		return fmt.Errorf("rewrite trimmed %s: %w", LogPath(), err)
	}
	return nil
}

// readTail returns the last n bytes of the log, or the whole file when it is
// shorter than n. Reading a fixed window from the end is what keeps the cost of
// reading logs independent of how long the daemon has been running.
func readTail(n int64) ([]byte, error) {
	f, err := os.Open(LogPath())
	if err != nil {
		return nil, err
	}
	defer f.Close()

	size, err := logSize()
	if err != nil {
		return nil, err
	}
	offset := max(int64(0), size-n)
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	tail := make([]byte, size-offset)
	if _, err := io.ReadFull(f, tail); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	return tail, nil
}

// moveHeadAside copies the first headBytes of the log into the previous
// generation. It overwrites that generation rather than appending, so a daemon
// that trims on every start cannot make gateway.log.1 grow without bound.
func moveHeadAside(headBytes int64) error {
	src, err := os.Open(LogPath())
	if err != nil {
		return fmt.Errorf("open %s for trim: %w", LogPath(), err)
	}
	defer src.Close()

	dstPath := LogPath() + ".1"
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, constants.FilePermFile)
	if err != nil {
		return fmt.Errorf("open %s: %w", dstPath, err)
	}
	defer dst.Close()

	if _, err := io.CopyN(dst, src, headBytes); err != nil {
		return fmt.Errorf("copy trimmed log head to %s: %w", dstPath, err)
	}
	return nil
}

// logSize reports the current log size, or 0 when there is no log to measure.
func logSize() (int64, error) {
	fi, err := os.Stat(LogPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	return fi.Size(), nil
}

// LogTail returns the last maxLines lines of the background run log.
//
// Only the tail of the file is read (logTailMaxBytes at most), not the whole
// log, so the cost does not grow with an installation that has been running for
// months. When the last maxLines lines do not fit in that window the seek lands
// mid-line; that fragment is dropped rather than shown as an entry nobody wrote,
// so the caller always gets whole lines.
func LogTail(maxLines int) string {
	data, err := readTail(logTailMaxBytes)
	if err != nil {
		return ""
	}

	// The read window lands mid-line whenever the log is larger than it, so the
	// first fragment is a line nobody wrote in that form. Drop it rather than
	// present it as an entry: everything after the newline is whole.
	if int64(len(data)) == logTailMaxBytes {
		if nl := bytes.IndexByte(data, '\n'); nl >= 0 {
			data = data[nl+1:]
		}
	}

	// A window with no newline at all is one very long line, or an empty log.
	// Splitting it yields a single element either way; the empty case is the one
	// that must not reach the caller as a phantom entry.
	if len(data) == 0 {
		return ""
	}

	lines := strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}
