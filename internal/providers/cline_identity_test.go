package providers

import (
	"strings"
	"testing"
)

func TestClineIdentityHeaders(t *testing.T) {
	h := ClineIdentityHeaders("1.2.3", nil)
	for k, want := range map[string]string{
		"HTTP-Referer":     "https://cline.bot",
		"X-Title":          "Cline",
		"User-Agent":       "9Router/1.2.3",
		"X-PLATFORM":       nodePlatform(),
		"X-CLIENT-TYPE":    "9router",
		"X-CLIENT-VERSION": "1.2.3",
		"X-CORE-VERSION":   "1.2.3",
		"X-IS-MULTIROOT":   "false",
	} {
		if h[k] != want {
			t.Errorf("%s = %q, want %q", k, h[k], want)
		}
	}
	if h["X-PLATFORM-VERSION"] == "" || !strings.HasPrefix(h["X-PLATFORM-VERSION"], "go") {
		t.Errorf("X-PLATFORM-VERSION = %q, want go runtime version", h["X-PLATFORM-VERSION"])
	}
	if _, has := h["Authorization"]; has {
		t.Error("identity must not carry Authorization")
	}
	// Extra headers merge (probe Accept).
	h = ClineIdentityHeaders("v", map[string]string{"Accept": "application/json"})
	if h["Accept"] != "application/json" || h["User-Agent"] != "9Router/v" {
		t.Fatalf("extra merge = %+v", h)
	}
	// Registry literals carry no stale impersonation.
	for _, p := range []string{"cline", "clinepass"} {
		for k, v := range KnownProviders[p].StaticHeaders {
			if strings.Contains(v, "Cline/") || v == "cline-cli" || v == "cli" {
				t.Errorf("%s StaticHeaders[%s] = %q: stale impersonation", p, k, v)
			}
		}
	}
}
