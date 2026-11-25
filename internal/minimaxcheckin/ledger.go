package minimaxcheckin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"9router/proxy/internal/constants"
)

// Ledger records the last check-in outcome per account in a small JSON file
// under DATA_DIR, so the dashboard can read the background worker's results
// without a second in-memory cache. Mirrors qodercheckin's ledger.
type Ledger struct {
	mu    sync.Mutex
	path  string
	byKey map[string]Result
}

// NewLedger opens (or starts) the ledger at path. A corrupted file must not
// brick gateway boot: it starts fresh and the next check-in rewrites it.
func NewLedger(path string) (*Ledger, error) {
	if path == "" {
		return nil, errors.New("minimaxcheckin: empty ledger path")
	}
	l := &Ledger{path: path, byKey: map[string]Result{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, fmt.Errorf("minimaxcheckin: read ledger: %w", err)
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &l.byKey); err != nil {
			l.byKey = map[string]Result{}
		}
	}
	return l, nil
}

func (l *Ledger) key(r Result) string {
	id := r.AccountID
	if id == "" {
		id = r.Account
	}
	return r.Provider + "\x00" + id
}

// Record stores one outcome atomically. A failed write must not flip an
// otherwise successful claim; the caller keeps the result either way.
func (l *Ledger) Record(r Result) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.byKey[l.key(r)] = r
	b, err := json.MarshalIndent(l.byKey, "", "  ")
	if err != nil {
		return fmt.Errorf("minimaxcheckin: marshal ledger: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(l.path), constants.FilePermDir); err != nil {
		return fmt.Errorf("minimaxcheckin: create ledger dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".minimax-checkin-*")
	if err != nil {
		return fmt.Errorf("minimaxcheckin: create temp ledger: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("minimaxcheckin: write ledger: %w", err)
	}
	if err := tmp.Chmod(constants.FilePermFile); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("minimaxcheckin: chmod ledger: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("minimaxcheckin: close ledger: %w", err)
	}
	if err := os.Rename(tmpName, l.path); err != nil {
		return fmt.Errorf("minimaxcheckin: replace ledger: %w", err)
	}
	return nil
}

// Results returns the last stored outcome per account, without network calls.
func (l *Ledger) Results() []Result {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Result, 0, len(l.byKey))
	for _, r := range l.byKey {
		out = append(out, r)
	}
	return out
}
