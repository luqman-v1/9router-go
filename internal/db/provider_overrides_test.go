package db

import "testing"

func TestProviderOverrides_SecuritySemantics(t *testing.T) {
	t.Run("rfc7230_tchar", func(t *testing.T) {
		for _, name := range []string{"X-Custom", "X-`Backtick`", "X-!#$%&'*+-.^_`|~"} {
			if _, err := NormalizeProviderOverrides(map[string]string{name: "v"}); err != nil {
				t.Errorf("accept tchar %q, got: %v", name, err)
			}
		}
	})
	t.Run("delimiter_rejection", func(t *testing.T) {
		for _, name := range []string{"X-T:value", "X T", "X-T\r\nInjected: 1"} {
			if _, err := NormalizeProviderOverrides(map[string]string{name: "v"}); err == nil {
				t.Errorf("expected error for name %q", name)
			}
		}
	})
	t.Run("nul_del_crlf_rejection", func(t *testing.T) {
		for _, val := range []string{"a\x00b", "a\x7fb", "a\rb", "a\nb", "a\x07b"} {
			if _, err := NormalizeProviderOverrides(map[string]string{"X-T": val}); err == nil {
				t.Errorf("expected error for %q", val)
			}
		}
		if _, err := NormalizeProviderOverrides(map[string]string{"X-T": "a\tb"}); err != nil {
			t.Errorf("HTAB must be accepted: %v", err)
		}
	})
	t.Run("case_conflict", func(t *testing.T) {
		if _, err := NormalizeProviderOverrides(map[string]string{"X-Token": "A", "x-token": "B"}); err == nil {
			t.Fatal("expected error on case-insensitive key conflict")
		}
		// Same value under two spellings is not a conflict: net/http sends one.
		if _, err := NormalizeProviderOverrides(map[string]string{"X-Token": "A", "x-token": "A"}); err != nil {
			t.Errorf("same value under two casings must be accepted: %v", err)
		}
	})
	t.Run("blocked_headers", func(t *testing.T) {
		for _, name := range []string{"Authorization", "Cookie", "Host", "Content-Length", "Transfer-Encoding"} {
			if _, err := NormalizeProviderOverrides(map[string]string{name: "v"}); err == nil {
				t.Errorf("expected %s to be blocked", name)
			}
			if !HeaderOverrideBlocked(name) {
				t.Errorf("HeaderOverrideBlocked must report %s blocked", name)
			}
		}
	})
}
