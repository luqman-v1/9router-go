package clientconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"9router/proxy/internal/log"
)

// The exported warning threshold has to reach the path that writes the file,
// or it is a constant with a doc comment and no effect.
//
// The spec forbids a hard cap (D7): the file belongs to the user, and refusing
// it is a refusal they cannot override. A warning is the shape that leaves the
// decision with them — and the cost is worth one: a 1 MiB config peaks at
// ~140 MB allocated inside the patcher's parsers.
func TestWriteFileAtomic_WarnsAboveLargeFileThreshold(t *testing.T) {
	tests := []struct {
		name  string
		size  int
		warns bool
	}{
		{"just under the threshold", LargeFileWarningBytes - 1, false},
		{"over the threshold", LargeFileWarningBytes + 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log.ClearConsoleLogs()
			base, _ := newBase(t)
			g := mustResolve(t, base, "settings.json")

			if err := WriteFileAtomic(g, bytes.Repeat([]byte("x"), tt.size), hooks{}); err != nil {
				t.Fatalf("WriteFileAtomic: %v", err)
			}

			got := filepath.Join(base, "settings.json")
			fi, err := os.Stat(got)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if fi.Size() != int64(tt.size) {
				t.Fatalf("wrote %d bytes, want %d", fi.Size(), tt.size)
			}

			warned := false
			for _, e := range log.ConsoleEntries() {
				if e.Level == log.LevelWarn && strings.Contains(e.Line, "large") {
					warned = true
				}
			}
			if warned != tt.warns {
				t.Errorf("warned = %v, want %v", warned, tt.warns)
			}
			// A warning is not a refusal: the file is written either way.
			if tt.warns && fi.Size() == 0 {
				t.Error("the file was refused; the threshold must warn, not block")
			}
		})
	}
}
