package providers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// freepiFixtureDir holds verbatim shapes of every api.freepi.ai response the
// free-pi provider depends on (see the README there for provenance).
const freepiFixtureDir = "testdata/freepi"

// TestFreePiFixturesAreValidJSON is the cheap guard: a fixture that does not
// parse is worse than no fixture, because the next reader trusts it.
func TestFreePiFixturesAreValidJSON(t *testing.T) {
	entries, err := os.ReadDir(freepiFixtureDir)
	if err != nil {
		t.Fatalf("read fixture dir: %v", err)
	}

	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(freepiFixtureDir, e.Name()))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
		})
		checked++
	}

	if checked == 0 {
		t.Fatal("no .json fixtures found; the contract record has gone missing")
	}
}

// TestFreePiContractInvariants pins the four facts the design leans on, so a
// fixture edited to match a changed upstream cannot silently disagree with the
// code that depends on it. Each assertion names the file it protects.
func TestFreePiContractInvariants(t *testing.T) {
	t.Run("consent version is 4", func(t *testing.T) {
		// src/consent.ts CONSENT_VERSION. The auth flow must submit exactly the
		// version the server expects, and the dashboard renders the matching text.
		var f struct {
			Version string `json:"version"`
			Text    string `json:"text"`
		}
		readFixture(t, "consent.json", &f)
		if f.Version != "4" {
			t.Errorf("consent version = %q, want %q", f.Version, FreePiConsentVersion)
		}
		if !strings.Contains(f.Text, "used to train models") {
			t.Error("consent text lost its training-data disclosure; the dashboard renders this verbatim")
		}
	})

	t.Run("session cap is 4", func(t *testing.T) {
		// server.mjs:958. We build for one session (conservative) but must not
		// forget the real ceiling, because 409 handling is keyed to it.
		var f struct {
			Status            int `json:"status"`
			ConcurrentSession struct {
				Code string `json:"code"`
			} `json:"concurrent_session"`
			SessionCap int `json:"_session_cap"`
		}
		readFixture(t, "error-concurrent-session.json", &f)
		if f.SessionCap != 4 {
			t.Errorf("session cap = %d, want 4", f.SessionCap)
		}
		if f.Status != 409 {
			t.Errorf("concurrent_session status = %d, want 409", f.Status)
		}
		if f.ConcurrentSession.Code != FreePiCodeConcurrentSession {
			t.Errorf("code = %q, want %q", f.ConcurrentSession.Code, FreePiCodeConcurrentSession)
		}
	})

	t.Run("429 taxonomy separates hourly from daily", func(t *testing.T) {
		var f struct {
			Hourly struct {
				Code  string `json:"code"`
				Retry bool   `json:"_retry_same_account"`
			} `json:"hourly_rate_limited"`
			Daily struct {
				Codes []string `json:"codes"`
				Retry bool     `json:"_retry_same_account"`
			} `json:"daily_quota_exhausted"`
		}
		readFixture(t, "error-429-taxonomy.json", &f)
		if f.Hourly.Code != FreePiCodeRateLimitExceeded {
			t.Errorf("hourly code = %q, want %q", f.Hourly.Code, FreePiCodeRateLimitExceeded)
		}
		if f.Hourly.Retry || f.Daily.Retry {
			t.Error("neither 429 class may be retried on the same account")
		}
		for _, want := range FreePiDailyQuotaCodes {
			found := false
			for _, got := range f.Daily.Codes {
				if got == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("daily-quota fixture is missing code %q", want)
			}
		}
	})

	t.Run("imp_url allowlist is the gravity domains", func(t *testing.T) {
		// SSRF hygiene: imp_url is upstream-flavoured input that we fetch
		// server-side, so the host is checked before any request.
		var f struct {
			ImpURLAllowlist struct {
				Hosts []string `json:"hosts"`
			} `json:"_imp_url_allowlist"`
		}
		readFixture(t, "ads.json", &f)
		if len(f.ImpURLAllowlist.Hosts) != len(FreePiImpressionHosts) {
			t.Fatalf("allowlist has %d hosts, want %d", len(f.ImpURLAllowlist.Hosts), len(FreePiImpressionHosts))
		}
		for i, want := range FreePiImpressionHosts {
			if f.ImpURLAllowlist.Hosts[i] != want {
				t.Errorf("host[%d] = %q, want %q", i, f.ImpURLAllowlist.Hosts[i], want)
			}
		}
	})

	t.Run("client-version floor is present", func(t *testing.T) {
		// Ignoring `min` bricks the provider with a permanent force-update
		// refusal, so the floor must be readable from the fixture at all.
		var f struct {
			Min    string `json:"min"`
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		}
		readFixture(t, "client-version.json", &f)
		if f.Min == "" {
			t.Error("no min floor; the version gate would go unnoticed")
		}
		if len(f.Models) == 0 || f.Models[0].ID == "" {
			t.Error("model catalog is empty; models[].id is the value a request must send")
		}
	})
}

func readFixture(t *testing.T, name string, into any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(freepiFixtureDir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
}
