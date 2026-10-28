package clientconfig

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// X5: a 64 KB file patches fast, does not panic, and keeps its tail.
func TestX5LargeFile(t *testing.T) {
	const target = 64 << 10
	for _, tt := range []struct {
		name   string
		format Format
		build  func() (string, Edit)
	}{
		{"jsonc", FormatJSONC, func() (string, Edit) {
			var b strings.Builder
			b.WriteString("{\n")
			for i := 0; b.Len() < target; i++ {
				fmt.Fprintf(&b, "  \"k%d\": \"v%d\",\n", i, i)
			}
			b.WriteString("  \"base_url\": \"https://old/v1\"\n}\n")
			return b.String(), Edit{KeyPath: []string{"base_url"}, Value: "http://new/v1"}
		}},
		{"toml", FormatTOML, func() (string, Edit) {
			var b strings.Builder
			b.WriteString("[t]\n")
			for i := 0; b.Len() < target; i++ {
				fmt.Fprintf(&b, "k%d = \"v%d\"\n", i, i)
			}
			b.WriteString("base_url = \"https://old/v1\"\n")
			return b.String(), Edit{KeyPath: []string{"t", "base_url"}, Value: "http://new/v1"}
		}},
		{"yaml", FormatYAML, func() (string, Edit) {
			var b strings.Builder
			for i := 0; b.Len() < target; i++ {
				fmt.Fprintf(&b, "k%d: \"v%d\"\n", i, i)
			}
			b.WriteString("base_url: \"https://old/v1\"\n")
			return b.String(), Edit{KeyPath: []string{"base_url"}, Value: "http://new/v1"}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			src, edit := tt.build()
			if len(src) < target {
				t.Fatalf("sample is only %d bytes", len(src))
			}
			begin := time.Now()
			out, err := Patch([]byte(src), tt.format, []Edit{edit})
			elapsed := time.Since(begin)
			if err != nil {
				t.Fatalf("Patch() error = %v", err)
			}
			if elapsed > x5Budget {
				t.Errorf("Patch() took %v on a %d byte file, budget %v", elapsed, len(src), x5Budget)
			}
			tail := tailAfter(src, `"https://old/v1"`)
			if tail == "" || !strings.HasSuffix(string(out), tail) {
				t.Error("suffix after the patched value changed")
			}
			if err := verifyNoError(t, out, tt.format, edit); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// tailAfter returns the bytes following the last occurrence of marker, which is
// the part of a sample that a splice must not touch.
func tailAfter(src, marker string) string {
	i := strings.LastIndex(src, marker)
	if i < 0 {
		return ""
	}
	return src[i+len(marker):]
}

func verifyNoError(t *testing.T, out []byte, format Format, edit Edit) error {
	t.Helper()
	again, err := Patch(out, format, []Edit{edit})
	if !errors.Is(err, ErrAlreadyPatched) {
		t.Errorf("re-patch error = %v, want ErrAlreadyPatched", err)
	}
	if !bytes.Equal(again, out) {
		t.Error("re-patch changed bytes")
	}
	return nil
}

func TestPatchNoEdits(t *testing.T) {
	src := []byte("{}")
	out, err := Patch(src, FormatJSONC, nil)
	if !errors.Is(err, ErrAlreadyPatched) {
		t.Fatalf("Patch() error = %v, want ErrAlreadyPatched", err)
	}
	if !bytes.Equal(out, src) {
		t.Fatalf("Patch() = %q, want the input back", out)
	}
}

func TestPatchUnknownFormat(t *testing.T) {
	if _, err := Patch([]byte("x"), Format(99), []Edit{{KeyPath: []string{"a"}, Value: "b"}}); !errors.Is(err, ErrUnsupportedStructure) {
		t.Fatalf("Patch() error = %v, want ErrUnsupportedStructure", err)
	}
}
