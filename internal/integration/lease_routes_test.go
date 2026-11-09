//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// absentDataDir is an absolute path that has no desktop install, so the
// broker is guaranteed absent on every host.
func absentDataDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "no-desktop-install")
}

// TestLeaseRoutesAreMountedOnTheRealRouter drives app.ProvideRouter, so it
// asserts the route table the binary serves. A handler that only a hand-built
// router reaches proves nothing: the dashboard calls these two paths through
// the production middleware stack, and a path typo is invisible until then.
func TestLeaseRoutesAreMountedOnTheRealRouter(t *testing.T) {
	env := newEnv(t)

	t.Run("lease-status answers for a supported provider", func(t *testing.T) {
		// A data dir with no desktop install: the broker is absent, so the
		// endpoint must answer 200 with found:false rather than 404. The
		// dashboard reads that shape to decide what to show.
		res := env.Get(t, "/api/oauth/minimax-code/lease-status?data_dir="+absentDataDir(t))
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
		}
		if !strings.Contains(string(res.Body), `"found":false`) {
			t.Errorf("body %q does not report found:false", truncate(res.Body))
		}
	})

	t.Run("lease-connect answers for a supported provider", func(t *testing.T) {
		// No broker is installed, so the connect must fail closed with 409
		// and the broker's code — and must not have written a connection.
		// The body is marshalled rather than concatenated: a Windows data
		// dir carries backslashes, and hand-built JSON would turn them into
		// invalid escapes.
		body, err := json.Marshal(map[string]string{"dataDir": absentDataDir(t)})
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		res := env.Post(t, "/api/oauth/minimax-code-global/lease-connect", body)
		if res.Status != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (body: %s)", res.Status, truncate(res.Body))
		}
		if !strings.Contains(string(res.Body), "BROKER_UNAVAILABLE") {
			t.Errorf("body %q does not carry the broker's code", truncate(res.Body))
		}
	})

	t.Run("lease-status is refused for an unsupported provider", func(t *testing.T) {
		// A 400 here, not a 404: the route exists, the provider does not
		// support lease auth. The dashboard distinguishes the two.
		res := env.Get(t, "/api/oauth/codex/lease-status")
		if res.Status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body: %s)", res.Status, truncate(res.Body))
		}
	})

	t.Run("lease-connect is refused for an unsupported provider", func(t *testing.T) {
		res := env.Post(t, "/api/oauth/claude/lease-connect", []byte(`{"dataDir":"/tmp"}`))
		if res.Status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body: %s)", res.Status, truncate(res.Body))
		}
	})
}
