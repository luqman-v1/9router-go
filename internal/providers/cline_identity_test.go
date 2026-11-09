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
	if h["X-PLATFORM-VERSION"] == "" || !strings.HasPrefix(h["X-PLATFORM-VERSION"], "v") {
		t.Errorf("X-PLATFORM-VERSION = %q, want a v-prefixed version", h["X-PLATFORM-VERSION"])
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

// TestNodePlatformFor pins the platform mapping on its own, without the
// implementation as its oracle: the Windows spelling is the whole point of
// the mapping, and a test that compares the header against nodePlatform()
// never exercises that branch on a host that is not Windows.
func TestNodePlatformFor(t *testing.T) {
	cases := []struct {
		goos string
		want string
	}{
		{"windows", "win32"},
		{"darwin", "darwin"},
		{"linux", "linux"},
	}
	for _, c := range cases {
		if got := nodePlatformFor(c.goos); got != c.want {
			t.Errorf("nodePlatformFor(%q) = %q, want %q", c.goos, got, c.want)
		}
	}
}
