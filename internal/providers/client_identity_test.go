package providers

import (
	"strconv"
	"strings"
	"testing"
)

// TestClientIdentitiesMeetUpstreamMinimums pins the two identity floors that
// upstream's proxies enforce outright. cli-chat-proxy.grok.com refuses a
// grok-shell identity below 1.0.13 with HTTP 426 before the request is routed,
// and the codex backend gates newer models on a CLI version it recognises.
//
// Both constants used to be literals scattered across six files (0.2.99 and
// 0.2.93), which is how grok-cli ended up serving an identity its own proxy
// rejects. Asserting the floor here means the next bump that regresses fails
// in unit tests instead of in production.
func TestClientIdentitiesMeetUpstreamMinimums(t *testing.T) {
	tests := []struct {
		name    string
		version string
		minimum string
	}{
		{name: "grok-cli", version: GrokCLIVersion, minimum: GrokCLIMinimumVersion},
		{name: "codex", version: CodexCLIVersion, minimum: "0.155.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if compareVersions(tt.version, tt.minimum) < 0 {
				t.Fatalf("%s identity %q is below the %q minimum its proxy enforces", tt.name, tt.version, tt.minimum)
			}
		})
	}
}

// compareVersions orders dotted numeric segments ("1.0.44" against "1.0.13").
// strconv cannot do this — these are four-segment versions, not floats — and
// the proxy compares them the same way, segment by segment.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		av, bv := versionSegment(as, i), versionSegment(bs, i)
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

// versionSegment reads one numeric segment, treating anything unparseable or
// absent as 0 so a short version like "1.0" does not sort above "1.0.1".
func versionSegment(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n, err := strconv.Atoi(parts[i])
	if err != nil {
		return 0
	}
	return n
}

// TestClientIdentityHeadersAreInternallyConsistent guards the other half of the
// contract: every header is derived from the one version literal. A header that
// still spells the version out would drift on the next bump, which is exactly
// how two different versions ended up shipping in one build.
func TestClientIdentityHeadersAreInternallyConsistent(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{name: "codex user-agent", header: CodexCLIUserAgent, want: "codex_cli_rs/" + CodexCLIVersion},
		{name: "codex version header", header: CodexCLIVersionHeader, want: CodexCLIVersion},
		{name: "grok user-agent", header: GrokCLIUserAgent, want: "grok-shell/" + GrokCLIVersion + " (linux; x86_64)"},
		{name: "grok pager user-agent", header: GrokCLIPagerUserAgent, want: "grok-pager/" + GrokCLIVersion + " grok-shell/" + GrokCLIVersion + " (linux; x86_64)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.header != tt.want {
				t.Errorf("header = %q, want %q", tt.header, tt.want)
			}
			if !strings.Contains(tt.header, GrokCLIVersion) && !strings.Contains(tt.header, CodexCLIVersion) {
				t.Errorf("header %q carries no version literal, so it will not move on the next bump", tt.header)
			}
		})
	}
}

// TestCodexStaticHeadersCarryVersion is the client-visible half: the codex
// backend reads the `version` header next to the User-Agent, and omitting it
// is what leaves newer models gated (upstream decolua/9router ca6e8407).
func TestCodexStaticHeadersCarryVersion(t *testing.T) {
	cfg, ok := KnownProviders["codex"]
	if !ok {
		t.Fatal("codex provider is missing from KnownProviders")
	}
	if got := cfg.StaticHeaders["User-Agent"]; got != CodexCLIUserAgent {
		t.Errorf("codex User-Agent = %q, want %q", got, CodexCLIUserAgent)
	}
	if got := cfg.StaticHeaders["version"]; got != CodexCLIVersionHeader {
		t.Errorf("codex version header = %q, want %q", got, CodexCLIVersionHeader)
	}
	if cfg.StaticHeaders["originator"] != "codex_cli_rs" {
		t.Errorf("codex originator = %q, want codex_cli_rs", cfg.StaticHeaders["originator"])
	}
}