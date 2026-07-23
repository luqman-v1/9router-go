package updater

import (
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		v1   string
		v2   string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.1.0", "1.0.0", 1},
		{"1.0.0", "1.1.0", -1},
		{"2.0.0", "1.9.9", 1},
		{"1.0.1", "1.0.0", 1},
		{"v1.2.3", "1.2.3", 0},
		{"v1.2.4", "v1.2.3", 1},
	}

	for _, tt := range tests {
		got := CompareVersions(tt.v1, tt.v2)
		if got != tt.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tt.v1, tt.v2, got, tt.want)
		}
	}
}

func TestGetCachedInfo(t *testing.T) {
	info := GetCachedInfo()
	if info == nil {
		t.Fatal("expected non-nil UpdateInfo")
	}
	if info.CurrentVersion == "" {
		t.Error("expected non-empty CurrentVersion")
	}
}
