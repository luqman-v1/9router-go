package dashboard

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubTailscaleHost pins both tailscale seams for the duration of the test.
// Without it these handlers exec the real binary, so the assertions below
// describe whatever state the developer's daemon happens to be in — and
// HandleTailscaleEnable would run `tailscale up --reset` against it.
func stubTailscaleHost(t *testing.T, bin string, exec func(ctx context.Context, bin string, args ...string) ([]byte, error)) {
	t.Helper()
	origBin, origExec := tailscaleBinFn, tailscaleExec
	tailscaleBinFn = func() string { return bin }
	tailscaleExec = exec
	t.Cleanup(func() {
		tailscaleBinFn, tailscaleExec = origBin, origExec
	})
}

func TestHandleTunnelEndpoints(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	h := NewDashboardHandler(repo)

	t.Run("TunnelStatus_Empty", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/tunnel/status", nil)
		rec := httptest.NewRecorder()

		h.HandleTunnelStatus(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}

		var res struct {
			Tunnel struct {
				Enabled   bool   `json:"enabled"`
				TunnelURL string `json:"tunnelUrl"`
				Running   bool   `json:"running"`
			} `json:"tunnel"`
			Tailscale struct {
				Enabled bool `json:"enabled"`
			} `json:"tailscale"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal status response: %v", err)
		}

		if res.Tunnel.Enabled || res.Tunnel.Running {
			t.Errorf("expected tunnel to be disabled and not running initially")
		}
	})

	t.Run("TunnelStatus_WithSettings", func(t *testing.T) {
		_ = repo.UpdateSettingsRaw(map[string]any{
			"tunnelEnabled": true,
			"tunnelUrl":     "https://test.trycloudflare.com",
		})

		req := httptest.NewRequest(http.MethodGet, "/api/tunnel/status", nil)
		rec := httptest.NewRecorder()

		h.HandleTunnelStatus(rec, req)

		var res struct {
			Tunnel struct {
				Enabled   bool   `json:"enabled"`
				TunnelURL string `json:"tunnelUrl"`
				Running   bool   `json:"running"`
			} `json:"tunnel"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal status response: %v", err)
		}

		if !res.Tunnel.Enabled || !res.Tunnel.Running {
			t.Errorf("expected tunnel to be enabled and running when url is configured")
		}
		if res.Tunnel.TunnelURL != "https://test.trycloudflare.com" {
			t.Errorf("unexpected tunnel URL: %s", res.Tunnel.TunnelURL)
		}
	})

	t.Run("TunnelEnable_ReturnsCleanError", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/tunnel/enable", nil)
		rec := httptest.NewRecorder()

		h.HandleTunnelEnable(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", rec.Code)
		}

		var res struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal error response: %v", err)
		}
		if res.Error == "" {
			t.Errorf("expected non-empty error message")
		}
	})

	t.Run("TunnelDisable_Success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/tunnel/disable", nil)
		rec := httptest.NewRecorder()

		h.HandleTunnelDisable(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}

		var res struct {
			Success bool `json:"success"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal disable response: %v", err)
		}
		if !res.Success {
			t.Errorf("expected success=true")
		}
	})

	t.Run("TailscaleCheck", func(t *testing.T) {
		stubTailscaleHost(t, "", func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("must not exec tailscale when none is installed")
			return nil, errors.New("unreachable")
		})

		req := httptest.NewRequest(http.MethodGet, "/api/tunnel/tailscale-check", nil)
		rec := httptest.NewRecorder()

		h.HandleTailscaleCheck(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}

		var res struct {
			Installed bool `json:"installed"`
			LoggedIn  bool `json:"loggedIn"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal check response: %v", err)
		}
		if res.Installed {
			t.Errorf("installed = true, want false when no binary is present")
		}
		if res.LoggedIn {
			t.Errorf("loggedIn = true, want false when no binary is present")
		}
	})

	t.Run("TailscaleEnable_ReturnsCleanError", func(t *testing.T) {
		// No binary on this host: the handler must refuse cleanly instead of
		// shelling out. This is the case the assertion is actually about, and
		// it used to be satisfied only on machines without Tailscale.
		stubTailscaleHost(t, "", func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("must not exec tailscale when none is installed")
			return nil, errors.New("unreachable")
		})

		req := httptest.NewRequest(http.MethodPost, "/api/tunnel/tailscale-enable", nil)
		rec := httptest.NewRecorder()

		h.HandleTailscaleEnable(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", rec.Code)
		}

		var res struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal error response: %v", err)
		}
		if res.Error == "" {
			t.Errorf("expected non-empty error message")
		}
	})

	t.Run("TailscaleEnable_NeedsLoginIsNotAnError", func(t *testing.T) {
		// Installed but logged out is not a failure: the dashboard needs the
		// auth URL to render its login button, so upstream answers 200 with
		// needsLogin. Collapsing it into a 400 would break that flow.
		stubTailscaleHost(t, "/usr/bin/tailscale", func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "status" {
				return []byte(`{"BackendState":"NoState"}`), nil
			}
			return []byte("To authenticate, visit: https://login.tailscale.com/a/abc123"), nil
		})

		req := httptest.NewRequest(http.MethodPost, "/api/tunnel/tailscale-enable", nil)
		rec := httptest.NewRecorder()

		h.HandleTailscaleEnable(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200 for a logged-out daemon, got %d (%s)", rec.Code, rec.Body.String())
		}

		var res struct {
			Success    bool   `json:"success"`
			NeedsLogin bool   `json:"needsLogin"`
			AuthURL    string `json:"authUrl"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal enable response: %v", err)
		}
		if res.Success || !res.NeedsLogin {
			t.Errorf("want success=false and needsLogin=true, got success=%v needsLogin=%v", res.Success, res.NeedsLogin)
		}
		if res.AuthURL != "https://login.tailscale.com/a/abc123" {
			t.Errorf("authUrl = %q, want the URL parsed out of the login output", res.AuthURL)
		}
	})

	t.Run("TailscaleDisable_Success", func(t *testing.T) {
		var resetCalls int
		stubTailscaleHost(t, "/usr/bin/tailscale", func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "funnel" {
				resetCalls++
			}
			return nil, nil
		})

		req := httptest.NewRequest(http.MethodPost, "/api/tunnel/tailscale-disable", nil)
		rec := httptest.NewRecorder()

		h.HandleTailscaleDisable(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
		if resetCalls != 1 {
			t.Errorf("funnel reset ran %d times, want exactly 1", resetCalls)
		}

		var res struct {
			Success bool `json:"success"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal disable response: %v", err)
		}
		if !res.Success {
			t.Errorf("expected success=true")
		}
	})
}
