package dashboard

import (
	"net/http"
	"testing"

	"9router/proxy/internal/providers"
)

// The G23b spec (docs/superpowers/specs/2026-10-19-mmf-selfhosted-spec.md §7)
// names two defects that a loopback caller could drive past the SSRF gate:
// a metadata endpoint reached through the local-caller bypass, and the MMF
// entry having no way to stay out of the provider pickers. Both are pinned
// here because neither had a failing test before.

// TestValidateProviderNode_LocalCallerStillBlockedFromCloudMetadata is the
// bypass the spec's Step 2 closes. nodeRequestIsLocal short-circuits the whole
// SSRF gate, so a browser on the same machine could point a node at the cloud
// metadata service and have the gateway dial it — 169.254.169.254 answers
// instance credentials to anything that asks from inside the VM.
//
// A loopback peer must still be refused for link-local metadata, while the
// legitimate self-hosted case (127.0.0.1:11434) keeps working: that contrast
// is what makes this a test of the metadata carve-out rather than of SSRF in
// general.
func TestValidateProviderNode_LocalCallerStillBlockedFromCloudMetadata(t *testing.T) {
	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://[fe80::1]/latest/meta-data/",
		"http://100.100.100.200/latest/meta-data/", // Alibaba
		"http://192.0.0.192/latest/meta-data/",     // Oracle
	} {
		t.Run(target, func(t *testing.T) {
			calls := staticProbeStub(t, http.StatusOK)

			rec, out := postValidateNode(t, `{"baseUrl":"`+target+`","apiKey":"k","type":"openai-compatible"}`, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
			if got := nodeOut(t, out, "error"); got != "URL not allowed" {
				t.Errorf("a local caller reached a metadata endpoint: error = %q, want %q", got, "URL not allowed")
			}
			if len(*calls) != 0 {
				t.Errorf("the gateway probed a metadata endpoint %d time(s)", len(*calls))
			}
		})
	}

	// The allowed half: a loopback caller keeps its self-hosted node.
	t.Run("loopback_self_hosted_still_allowed", func(t *testing.T) {
		calls := staticProbeStub(t, http.StatusOK)
		rec, out := postValidateNode(t, `{"baseUrl":"http://127.0.0.1:11434","apiKey":"k","type":"openai-compatible"}`, true)
		if !nodeValid(t, out) {
			t.Fatalf("self-hosted loopback node was refused: %s", rec.Body.String())
		}
		if len(*calls) == 0 {
			t.Fatal("expected a probe for the self-hosted node")
		}
	})
}

// TestValidateProviderNode_LocalCallerBlockedFromMetadataSpellings covers the
// ways a metadata endpoint can be written when the caller is local. The literal
// dotted form is refused above; these are the spellings a string comparison
// misses — the address as one decimal integer, GCP's documented hostname, and a
// wildcard-DNS name that resolves to the address from anywhere. All three are
// refused without any DNS answer, so the test holds with the network off.
func TestValidateProviderNode_LocalCallerBlockedFromMetadataSpellings(t *testing.T) {
	for _, target := range []string{
		"http://2852039166/latest/meta-data/",               // 169.254.169.254 decimal
		"http://metadata.google.internal/latest/meta-data/", // GCP documented name
		"http://169.254.169.254.nip.io/latest/meta-data/",   // wildcard DNS, dotted
		"http://169-254-169-254.sslip.io/latest/meta-data/", // wildcard DNS, dashed
	} {
		t.Run(target, func(t *testing.T) {
			calls := staticProbeStub(t, http.StatusOK)

			rec, out := postValidateNode(t, `{"baseUrl":"`+target+`","apiKey":"k","type":"openai-compatible"}`, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
			if got := nodeOut(t, out, "error"); got != "URL not allowed" {
				t.Errorf("a local caller reached metadata via %s: error = %q, want %q", target, got, "URL not allowed")
			}
			if len(*calls) != 0 {
				t.Errorf("the gateway probed %s %d time(s)", target, len(*calls))
			}
		})
	}

	// The control: an ordinary public address written the same way stays usable,
	// so the refusals above are about the metadata range, not about rejecting
	// unusual spellings wholesale.
	t.Run("decimal_public_ip_still_allowed", func(t *testing.T) {
		calls := staticProbeStub(t, http.StatusOK)
		rec, out := postValidateNode(t, `{"baseUrl":"http://134744072/v1","apiKey":"k","type":"openai-compatible"}`, true) // 8.8.8.8
		if !nodeValid(t, out) {
			t.Fatalf("public address in decimal was refused: %s", rec.Body.String())
		}
		if len(*calls) == 0 {
			t.Fatal("expected a probe for the public node")
		}
	})
}

// TestKnownProviders_MMFIsHidden pins Step 1 of the same spec: mimo-free is a
// no-auth provider that must stay reachable (connections resolve it) while
// staying out of the lists a person scans. Without a Hidden field on the
// config there is nothing for a listing path to consult, so every picker that
// iterates KnownProviders shows it.
func TestKnownProviders_MMFIsHidden(t *testing.T) {
	cfg, ok := providers.KnownProviders["mimo-free"]
	if !ok {
		t.Fatal("mimo-free missing from the registry")
	}
	if !cfg.Hidden {
		t.Error("mimo-free is listed in the provider pickers; it must declare Hidden")
	}
	if !cfg.NoAuth {
		t.Error("mimo-free lost NoAuth: connections with no key would be refused")
	}

	// The flag must not leak onto providers a person is expected to connect.
	for _, id := range []string{"claude", "deepseek", "openai"} {
		c, ok := providers.KnownProviders[id]
		if !ok {
			continue
		}
		if c.Hidden {
			t.Errorf("%s is hidden; only the MMF family should be", id)
		}
	}
}

// TestValidateProviderNode_SpecAcceptanceMatrix walks the acceptance matrix in
// §6 of the G23b spec one row at a time. The rows that already had a test above
// are the metadata carve-out; these are the remaining two: a local caller may
// point a node at a LAN address, and no caller may use a scheme outside
// http/https.
func TestValidateProviderNode_SpecAcceptanceMatrix(t *testing.T) {
	t.Run("local_caller_allows_lan_private_ip", func(t *testing.T) {
		for _, target := range []string{
			"http://192.168.1.50:11434",
			"http://10.0.0.7:8000/v1",
		} {
			calls := staticProbeStub(t, http.StatusOK)
			rec, out := postValidateNode(t, `{"baseUrl":"`+target+`","apiKey":"k","type":"openai-compatible"}`, true)
			if !nodeValid(t, out) {
				t.Errorf("local caller refused LAN node %s: %s", target, rec.Body.String())
			}
			if len(*calls) == 0 {
				t.Errorf("no probe ran for LAN node %s", target)
			}
		}
	})

	t.Run("remote_caller_blocked_from_lan_private_ip", func(t *testing.T) {
		for _, target := range []string{
			"http://192.168.1.50:11434",
			"http://10.0.0.7:8000/v1",
		} {
			calls := staticProbeStub(t, http.StatusOK)
			_, out := postValidateNode(t, `{"baseUrl":"`+target+`","apiKey":"k","type":"openai-compatible"}`, false)
			if got := nodeOut(t, out, "error"); got != "URL not allowed" {
				t.Errorf("remote caller reached LAN node %s: error = %q", target, got)
			}
			if len(*calls) != 0 {
				t.Errorf("the gateway probed LAN node %s for a remote caller", target)
			}
		}
	})

	t.Run("non_http_scheme_refused", func(t *testing.T) {
		for _, target := range []string{
			"ftp://example.com/x",
			"file:///etc/passwd",
			"gopher://example.com:70/",
		} {
			calls := staticProbeStub(t, http.StatusOK)
			_, out := postValidateNode(t, `{"baseUrl":"`+target+`","apiKey":"k","type":"openai-compatible"}`, true)
			if got := nodeOut(t, out, "error"); got != "Invalid URL format" {
				t.Errorf("scheme of %s was accepted: error = %q", target, got)
			}
			if len(*calls) != 0 {
				t.Errorf("the gateway probed a non-http URL: %s", target)
			}
		}
	})
}
