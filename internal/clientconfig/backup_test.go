package clientconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixedClock(ts time.Time) func() time.Time {
	return func() time.Time { return ts }
}

func openTestRoot(t *testing.T, base string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(base)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// B1: two backups in a row are two files, and the first keeps its own bytes.
func TestWriteExclusiveBackup_SecondCallDoesNotOverwriteFirst(t *testing.T) {
	base, _ := newBase(t)
	r := openTestRoot(t, base)
	clock := fixedClock(time.Date(2026, 10, 19, 10, 15, 32, 123456789, time.UTC))

	first, err := writeExclusiveBackup(r, "settings.json", []byte("v1"), clock)
	if err != nil {
		t.Fatalf("first backup: %v", err)
	}
	second, err := writeExclusiveBackup(r, "settings.json", []byte("v2"), clock)
	if err != nil {
		t.Fatalf("second backup: %v", err)
	}
	if first == second {
		t.Fatalf("both calls returned %q", first)
	}

	got, err := os.ReadFile(filepath.Join(base, first))
	if err != nil {
		t.Fatalf("read first: %v", err)
	}
	if string(got) != "v1" {
		t.Errorf("first backup content = %q, want v1", got)
	}
}

// B2: a pre-existing name with the exact timestamp is skipped, not replaced.
func TestWriteExclusiveBackup_CollisionPicksSuffix(t *testing.T) {
	base, _ := newBase(t)
	r := openTestRoot(t, base)
	ts := time.Date(2026, 10, 19, 10, 15, 32, 123456789, time.UTC)

	taken := "settings.json.bak." + ts.Format(backupStamp)
	if err := os.WriteFile(filepath.Join(base, taken), []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	name, err := writeExclusiveBackup(r, "settings.json", []byte("new"), fixedClock(ts))
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if want := taken + "-1"; name != want {
		t.Errorf("name = %q, want %q", name, want)
	}
	if got, _ := os.ReadFile(filepath.Join(base, taken)); string(got) != "old" {
		t.Errorf("pre-existing backup was modified: %q", got)
	}
}

// B3: exhausting every candidate name is a typed error and creates nothing.
func TestWriteExclusiveBackup_CollisionExhausted(t *testing.T) {
	base, _ := newBase(t)
	r := openTestRoot(t, base)
	ts := time.Date(2026, 10, 19, 10, 15, 32, 123456789, time.UTC)

	stem := "settings.json.bak." + ts.Format(backupStamp)
	for i := 0; i < backupAttempts; i++ {
		name := stem
		if i > 0 {
			name = stem + "-" + itoa(i)
		}
		if err := os.WriteFile(filepath.Join(base, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	_, err := writeExclusiveBackup(r, "settings.json", []byte("new"), fixedClock(ts))
	if !errors.Is(err, ErrBackupCollision) {
		t.Fatalf("err = %v, want ErrBackupCollision", err)
	}

	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != backupAttempts {
		t.Errorf("directory has %d entries, want the %d seeded ones only", len(entries), backupAttempts)
	}
}

func TestWriteExclusiveBackup_NameHasNoColon(t *testing.T) {
	base, _ := newBase(t)
	r := openTestRoot(t, base)
	name, err := writeExclusiveBackup(r, "a.json", []byte("x"),
		fixedClock(time.Date(2026, 10, 19, 10, 15, 32, 0, time.UTC)))
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if strings.Contains(name, ":") {
		t.Errorf("name %q contains a colon, which Windows rejects and ADS interprets", name)
	}
	if !strings.HasSuffix(name, "Z") {
		t.Errorf("name %q does not end in Z", name)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
