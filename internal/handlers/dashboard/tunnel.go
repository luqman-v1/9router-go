package dashboard

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/config"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/tunnel"
)

var unixTailscaleCandidates = []string{
	"/usr/local/bin/tailscale",
	"/opt/homebrew/bin/tailscale",
	"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
	"/usr/sbin/tailscale",
	"/usr/bin/tailscale",
	"/snap/bin/tailscale",
}

// Package-level seams so tests can pin the host instead of depending on what
// is installed on the machine running them. A dashboard test that shells out
// to the developer's real tailscale is not a unit test: it reads whatever
// state that daemon happens to be in and, via `tailscale up --reset`, mutates
// it.
var (
	tailscaleBinFn = findTailscaleBin
	tailscaleExec  = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, bin, args...).CombinedOutput()
	}
)

// findTailscaleBin searches for the tailscale executable across PATH and well-known locations.
func findTailscaleBin() string {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	for _, cand := range unixTailscaleCandidates {
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	if runtime.GOOS == "windows" {
		candidates := []string{
			`C:\Program Files\Tailscale\tailscale.exe`,
			`C:\Program Files (x86)\Tailscale\tailscale.exe`,
		}
		for _, cand := range candidates {
			if _, err := os.Stat(cand); err == nil {
				return cand
			}
		}
	}
	return ""
}

// hasBrew checks if Homebrew is available on macOS.
func hasBrew() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	if _, err := exec.LookPath("brew"); err == nil {
		return true
	}
	for _, p := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// TailscaleStatusJSON represents the structure output by `tailscale status --json`.
type TailscaleStatusJSON struct {
	BackendState string `json:"BackendState"`
	AuthURL      string `json:"AuthURL"`
	Self         struct {
		DNSName string `json:"DNSName"`
		Online  bool   `json:"Online"`
	} `json:"Self"`
}

// probeTailscaleStatus executes `tailscale status --json` with a short timeout.
func probeTailscaleStatus(ctx context.Context, bin string) (*TailscaleStatusJSON, error) {
	if bin == "" {
		return nil, errors.New("tailscale binary not found")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := tailscaleExec(ctx, bin, "status", "--json")
	if err != nil {
		return nil, err
	}
	var st TailscaleStatusJSON
	if err := json.Unmarshal(out, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// HandleTunnelStatus handles GET /api/tunnel/status.
// Returns current state of Cloudflare Tunnel and Tailscale Funnel.
func (h *DashboardHandler) HandleTunnelStatus(w http.ResponseWriter, r *http.Request) {
	raw, err := h.Repo.GetSettingsRaw()
	if err != nil || raw == nil {
		raw = map[string]any{}
	}
	tunnelEnabled, _ := raw["tunnelEnabled"].(bool)
	// Fallback only. The tunnel state file is the authority for the live
	// hostname and Status() prefers it; this value is read solely so a gateway
	// restored from a database backup — with settings intact but no state file —
	// still has an address to show. Nothing writes this key any more, so it is
	// empty on every gateway that has enabled a tunnel since.
	tunnelURL, _ := raw["tunnelUrl"].(string)
	tunnelCustomDomain, _ := raw["tunnelCustomDomain"].(string)
	tailscaleEnabled, _ := raw["tailscaleEnabled"].(bool)
	tailscaleURL, _ := raw["tailscaleUrl"].(string)
	bin := tailscaleBinFn()
	tailscaleRunning := false
	tailscaleLoggedIn := false
	if bin != "" {
		if st, err := probeTailscaleStatus(r.Context(), bin); err == nil && st != nil {
			tailscaleLoggedIn = st.BackendState == "Running"
			tailscaleRunning = tailscaleLoggedIn
			if tailscaleURL == "" && st.Self.DNSName != "" {
				tailscaleURL = "https://" + strings.TrimSuffix(st.Self.DNSName, ".")
			}
		}
	} else if tailscaleEnabled && tailscaleURL != "" {
		tailscaleRunning = true
	}
	// The Cloudflare status comes from the tunnel package, which probes the
	// recorded PID rather than trusting the stored URL: a settings flag with
	// no live process is a dead tunnel, and the dashboard must show that.
	ts := tunnel.Status(tunnelEnabled, tunnelURL, tunnelCustomDomain)
	// The stored credentials are returned so the form can be repopulated. They
	// are sent over the dashboard session, which is already authenticated, and
	// they are what makes a restore usable: without them the operator sees
	// blank token fields next to a connected tunnel and has to find the token
	// again before restarting anything.
	tunnelToken, _ := raw["tunnelToken"].(string)
	cfApiToken, _ := raw["tunnelCfApiToken"].(string)
	tailscaleKey, _ := raw["tailscaleAuthKey"].(string)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"tunnel": map[string]any{
			"enabled":         ts.Enabled,
			"settingsEnabled": ts.SettingsEnabled,
			"tunnelUrl":       ts.TunnelURL,
			"shortId":         ts.ShortID,
			"publicUrl":       ts.PublicURL,
			"running":         ts.Running,
			"mode":            ts.Mode,
			"customDomain":    ts.CustomDomain,
			"workerUrl":       tunnel.WorkerURL(),
			"previousUrl":     ts.PreviousURL,
			"rotatedAt":       ts.RotatedAt,
			"state":           ts.State,
			"token":           tunnelToken,
			"cfApiToken":      cfApiToken,
		},
		"tailscale": map[string]any{
			"enabled":         tailscaleEnabled,
			"settingsEnabled": tailscaleEnabled,
			"tunnelUrl":       tailscaleURL,
			"running":         tailscaleRunning && tailscaleEnabled,
			"loggedIn":        tailscaleLoggedIn,
			"authKey":         tailscaleKey,
		},
		"download": map[string]any{
			"downloading": false,
			"progress":    0,
		},
	})
}

// tunnelEnableRequest is the optional body of POST /api/tunnel/enable. Every
// field is optional so a caller that posts nothing still gets a quick tunnel.
type tunnelEnableRequest struct {
	ShortID      string `json:"shortId"`
	Token        string `json:"token"`
	CustomDomain string `json:"customDomain"`
	Protocol     string `json:"protocol"`
	// CFAPIToken switches the enable into auto-provisioning: the tunnel, its
	// ingress rule and its DNS record are created from this token, so no
	// manual Cloudflare dashboard step is left.
	CFAPIToken string `json:"cfApiToken"`
	// CFAccountID is optional; when empty the first account the token reaches
	// is used.
	CFAccountID string `json:"cfAccountId"`
	// CFZone is the domain hosting the hostname, e.g. "example.com".
	CFZone string `json:"cfZone"`
}

// HandleTunnelEnable handles POST /api/tunnel/enable.
//
// Mirrors upstream enableTunnel: reuse a healthy tunnel, otherwise download
// cloudflared if needed, spawn it, register the shortId, and wait until the
// public URL answers before reporting success.
func (h *DashboardHandler) HandleTunnelEnable(w http.ResponseWriter, r *http.Request) {
	var req tunnelEnableRequest
	// An empty body is valid — it means "quick tunnel, generated id".
	if r.Body != nil {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		_ = json.Unmarshal(body, &req)
	}

	// The tunnel publishes the gateway to the internet, so it must not start
	// while the dashboard still accepts unauthenticated requests or sits on
	// its default password. The same gate the frontend applies, enforced here
	// so a direct API call cannot bypass it.
	raw, err := h.Repo.GetSettingsRaw()
	if err != nil || raw == nil {
		raw = map[string]any{}
	}
	if gateErr := tunnelSafetyGate(raw); gateErr != nil {
		writePlainError(w, http.StatusBadRequest, gateErr.Error())
		return
	}

	// The mapping service is read here rather than inside the tunnel package
	// because the settings row is this handler's, not the package's. Applying
	// it before Enable means a rotation mid-tunnel registers with the same
	// service the initial enable used.
	if v, ok := raw["tunnelWorkerUrl"].(string); ok {
		tunnel.SetWorkerURL(v)
	}

	// Both credentials are persisted so a restored configuration works without
	// being set up again. The tunnel token is what `cloudflared tunnel run`
	// authenticates with, and the API token is what Auto Setup provisions with;
	// neither is echoed back by the status endpoint, so without storing them a
	// restore would leave a named tunnel that cannot be restarted.
	if strings.TrimSpace(req.Token) != "" {
		if err := h.Repo.UpdateSettingsRaw(map[string]any{"tunnelToken": req.Token}); err != nil {
			log.Warn("tunnel", "could not persist tunnel token; a restore will need it re-entered", "error", err)
		}
	}
	if strings.TrimSpace(req.CFAPIToken) != "" {
		if err := h.Repo.UpdateSettingsRaw(map[string]any{"tunnelCfApiToken": req.CFAPIToken}); err != nil {
			log.Warn("tunnel", "could not persist Cloudflare API token; Auto Setup will need it re-entered", "error", err)
		}
	}

	res, err := tunnel.Enable(r.Context(), tunnel.Options{
		CustomShortID: req.ShortID,
		Token:         req.Token,
		CustomDomain:  req.CustomDomain,
		Protocol:      req.Protocol,
		Cloudflare: tunnel.CloudflareOptions{
			APIToken:  req.CFAPIToken,
			AccountID: req.CFAccountID,
			Zone:      req.CFZone,
			Hostname:  req.CustomDomain,
		},
	})
	if err != nil {
		// The dashboard shows this message in a toast that disappears; the
		// console log is the only place the full cause survives, so it is
		// written here as well as inside the package.
		log.Error("tunnel", "enable failed", "error", err, "shortId", req.ShortID, "mode", modeOf(req))
		writePlainError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Info("tunnel", "enable succeeded", "tunnelUrl", res.TunnelURL, "publicUrl", res.PublicURL, "shortId", res.ShortID)

	// The URL is deliberately not stored here. The tunnel state file is the only
	// record of the live hostname, and a copy in settings went stale on the first
	// rotation — which is what left the login gate deciding access for an address
	// the tunnel no longer served.
	_ = h.Repo.UpdateSettingsRaw(map[string]any{
		"tunnelEnabled":      true,
		"tunnelCustomDomain": req.CustomDomain,
	})
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":        true,
		"tunnelUrl":      res.TunnelURL,
		"shortId":        res.ShortID,
		"publicUrl":      res.PublicURL,
		"mode":           res.Mode,
		"alreadyRunning": res.AlreadyRun,
	})
}

// modeOf reports which tunnel mode a request asks for, for logging. An empty
// token means quick tunnel, which is the default; a Cloudflare API token means
// auto-provisioning, which takes precedence because it creates the tunnel the
// connector token would otherwise name.
func modeOf(req tunnelEnableRequest) string {
	switch {
	case strings.TrimSpace(req.CFAPIToken) != "":
		return tunnel.ModeAuto
	case strings.TrimSpace(req.Token) != "":
		return tunnel.ModeNamed
	default:
		return tunnel.ModeQuick
	}
}

// tunnelSafetyGate refuses to expose the gateway while login or the API key is
// unconfigured. An internet-reachable dashboard on its default password is the
// one configuration that must never happen by accident.
func tunnelSafetyGate(raw map[string]any) error {
	requireLogin := true
	if v, ok := raw["requireLogin"].(bool); ok {
		requireLogin = v
	}
	requireAPIKey := false
	if v, ok := raw["requireApiKey"].(bool); ok {
		requireAPIKey = v
	}
	if !requireLogin {
		return errors.New("security required: enable \"Require login\" and set a custom password before activating the tunnel")
	}
	if !requireAPIKey {
		return errors.New("security required: enable \"Require API key\" before activating the tunnel")
	}
	return nil
}

// HandleTunnelDisable handles POST /api/tunnel/disable.
func (h *DashboardHandler) HandleTunnelDisable(w http.ResponseWriter, r *http.Request) {
	if err := tunnel.Disable(); err != nil {
		writePlainError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = h.Repo.UpdateSettingsRaw(map[string]any{
		"tunnelEnabled": false,
	})
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// tailscaleLoginWithKey authenticates this node with an auth key.
//
// `--authkey` is passed as an argument rather than through the environment
// because tailscaled reads it from the command line on `up`. The key is not
// logged: it appears in the process table either way, but putting it in the
// console log as well would leave it in a file anyone with log access can read.
func tailscaleLoginWithKey(ctx context.Context, bin, authKey string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	out, err := tailscaleExec(ctx, bin, "up", "--authkey="+authKey, "--accept-routes")
	if err != nil {
		return fmt.Errorf("tailscale up with auth key: %s", strings.TrimSpace(string(out)))
	}
	// A key that is expired, revoked, or already consumed makes `up` exit
	// non-zero with a message. Confirm the node actually came up rather than
	// trusting the exit code alone.
	st, statusErr := probeTailscaleStatus(ctx, bin)
	if statusErr != nil || st == nil || st.BackendState != "Running" {
		return fmt.Errorf("node did not come up after key authentication")
	}
	return nil
}

// HandleTailscaleCheck handles GET /api/tunnel/tailscale-check.
// Checks Tailscale installation, login state, and daemon status, matching upstream route.js.
func (h *DashboardHandler) HandleTailscaleCheck(w http.ResponseWriter, r *http.Request) {
	bin := tailscaleBinFn()
	if bin == "" {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"installed":     false,
			"loggedIn":      false,
			"platform":      runtime.GOOS,
			"brewAvailable": hasBrew(),
			"daemonRunning": false,
		})
		return
	}
	st, err := probeTailscaleStatus(r.Context(), bin)
	daemonRunning := err == nil && st != nil
	loggedIn := daemonRunning && st.BackendState == "Running"
	tunnelURL := ""
	if loggedIn && st.Self.DNSName != "" {
		tunnelURL = "https://" + strings.TrimSuffix(st.Self.DNSName, ".")
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"installed":     true,
		"loggedIn":      loggedIn,
		"platform":      runtime.GOOS,
		"brewAvailable": hasBrew(),
		"daemonRunning": daemonRunning,
		"tunnelUrl":     tunnelURL,
	})
}

// tailscaleEnableRequest is the optional body of POST /api/tunnel/tailscale-enable.
// An empty body keeps the interactive browser login, which is what an operator
// on a desktop expects.
type tailscaleEnableRequest struct {
	// AuthKey authenticates this machine without a browser. It is the only way
	// to bring a headless gateway up — a server, a container, or a Pi has
	// nowhere to open a login URL.
	AuthKey string `json:"authKey"`
}

// HandleTailscaleEnable handles POST /api/tunnel/tailscale-enable.
// Mirrors upstream enableTailscale: verifies login, triggers login flow if needed, and starts background Funnel.
//
// An auth key takes a different path from the interactive login: instead of
// returning a URL for the operator to visit, it authenticates the node
// directly and the enable continues straight to the funnel. That is what makes
// a headless gateway reachable at all.
func (h *DashboardHandler) HandleTailscaleEnable(w http.ResponseWriter, r *http.Request) {
	var req tailscaleEnableRequest
	if r.Body != nil {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 16<<10))
		_ = json.Unmarshal(body, &req)
	}
	authKey := strings.TrimSpace(req.AuthKey)

	// A key supplied now is persisted so the watchdog can re-authenticate a
	// node whose key expired. Without it a headless gateway that loses its
	// session stays down until someone can reach a browser — which is exactly
	// the situation the key was meant to avoid.
	if authKey != "" {
		if err := h.Repo.UpdateSettingsRaw(map[string]any{"tailscaleAuthKey": authKey}); err != nil {
			log.Warn("tunnel", "could not persist tailscale auth key; the watchdog will not be able to re-authenticate", "error", err)
		}
	} else {
		// Fall back to a stored key so a re-enable after a restart does not
		// demand a browser again.
		if raw, sErr := h.Repo.GetSettingsRaw(); sErr == nil && raw != nil {
			authKey, _ = raw["tailscaleAuthKey"].(string)
		}
	}

	bin := tailscaleBinFn()
	if bin == "" {
		writePlainError(w, http.StatusBadRequest, "Tailscale CLI is not installed or detected on your system path")
		return
	}

	if authKey != "" {
		if err := tailscaleLoginWithKey(r.Context(), bin, authKey); err != nil {
			// A rejected key is reported as such rather than falling through to
			// the browser flow: the operator asked for key auth, and silently
			// opening a login URL instead would leave them waiting on a browser
			// that never appears on a headless machine.
			writePlainError(w, http.StatusBadRequest, fmt.Sprintf("Tailscale auth key rejected: %s", strings.TrimSpace(err.Error())))
			return
		}
	} else {
		st, err := probeTailscaleStatus(r.Context(), bin)
		if err != nil || st == nil || st.BackendState != "Running" {
			authURL := ""
			if st != nil && st.AuthURL != "" {
				authURL = st.AuthURL
			} else {
				// Trigger login flow to obtain AuthURL
				upCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
				defer cancel()
				out, _ := tailscaleExec(upCtx, bin, "up", "--reset")
				re := regexp.MustCompile(`https://login\.tailscale\.com/[^\s]+`)
				if match := re.FindString(string(out)); match != "" {
					authURL = match
				}
			}
			if authURL != "" {
				handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
					"success":    false,
					"needsLogin": true,
					"authUrl":    authURL,
				})
				return
			}
			writePlainError(w, http.StatusBadRequest, "Tailscale daemon is not running or logged in. Run 'tailscale up' to log in.")
			return
		}
	}

	// Reset any existing funnel before starting a fresh one
	_, _ = tailscaleExec(context.Background(), bin, "funnel", "--bg", "reset")
	port := config.LoadConfig().Port
	if port <= 0 {
		port = 20130
	}
	funnelCtx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out, err := tailscaleExec(funnelCtx, bin, "funnel", "--bg", strconv.Itoa(port))
	outStr := string(out)
	if strings.Contains(outStr, "Funnel is not enabled") {
		enableURL := "https://login.tailscale.com/admin/settings/features"
		re := regexp.MustCompile(`https://login\.tailscale\.com/[^\s]+`)
		if match := re.FindString(outStr); match != "" {
			enableURL = match
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success":          false,
			"funnelNotEnabled": true,
			"enableUrl":        enableURL,
		})
		return
	}
	if err != nil && !strings.Contains(outStr, "available at") {
		writePlainError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to start Tailscale funnel: %s", strings.TrimSpace(outStr)))
		return
	}
	// Retrieve actual public DNS name from status. A fresh probe is taken
	// rather than reusing an earlier one because the funnel start can change
	// what the node reports, and the auth-key path has no earlier probe at all.
	stFresh, err := probeTailscaleStatus(r.Context(), bin)
	if err != nil || stFresh == nil || stFresh.Self.DNSName == "" {
		writePlainError(w, http.StatusInternalServerError, "Tailscale funnel started but failed to retrieve DNS name")
		return
	}
	tunnelURL := "https://" + strings.TrimSuffix(stFresh.Self.DNSName, ".")
	_ = h.Repo.UpdateSettingsRaw(map[string]any{
		"tailscaleEnabled": true,
		"tailscaleUrl":     tunnelURL,
	})
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"tunnelUrl": tunnelURL,
	})
}

// HandleTunnelRepairOrigin handles POST /api/tunnel/repair-origin.
//
// It rewrites the tunnel's ingress rule so the origin points at this gateway's
// real port. The failure it fixes is invisible in the dashboard: the connector
// process is up, the hostname resolves, and the only symptom is a health-check
// timeout — because the ingress rule still names the port of whatever the
// operator ran before (upstream Node's 20128, typically).
//
// Only a remotely-managed tunnel can be repaired here, because only that kind
// stores its configuration on Cloudflare where the API can reach it. A
// locally-managed tunnel keeps its ingress in a YAML file on the origin, and
// the error says so rather than reporting a generic failure.
func (h *DashboardHandler) HandleTunnelRepairOrigin(w http.ResponseWriter, r *http.Request) {
	raw, err := h.Repo.GetSettingsRaw()
	if err != nil || raw == nil {
		raw = map[string]any{}
	}

	token, _ := raw["tunnelCfApiToken"].(string)
	if strings.TrimSpace(token) == "" {
		writePlainError(w, http.StatusBadRequest,
			"repairing the origin needs the Cloudflare API token saved from Auto Setup — it is what authorises the change")
		return
	}

	hostname, _ := raw["tunnelCustomDomain"].(string)
	accountID, _ := raw["tunnelCfAccountId"].(string)
	zone, _ := raw["tunnelCfZone"].(string)
	if strings.TrimSpace(hostname) == "" {
		writePlainError(w, http.StatusBadRequest, "no public hostname is configured, so there is no ingress rule to repair")
		return
	}

	port := config.LoadConfig().Port
	if port <= 0 {
		port = 20130
	}

	opts := tunnel.CloudflareOptions{
		APIToken:  token,
		AccountID: accountID,
		Zone:      zone,
		Hostname:  hostname,
	}

	tunnelID, findErr := tunnel.FindTunnelIDByName(r.Context(), opts)
	if findErr != nil {
		writePlainError(w, http.StatusInternalServerError, fmt.Sprintf("could not find the tunnel on Cloudflare: %s", findErr.Error()))
		return
	}

	if err := tunnel.RepairTunnelIngress(r.Context(), opts, tunnelID, port); err != nil {
		log.Error("tunnel", "origin repair failed", "hostname", hostname, "port", port, "error", err)
		writePlainError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info("tunnel", "origin repaired", "hostname", hostname, "port", port, "tunnelId", tunnelID)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"hostname": hostname,
		"port":     port,
		"tunnelId": tunnelID,
	})
}

// HandleTailscaleDisable handles POST /api/tunnel/tailscale-disable.
// Mirrors upstream disableTailscale: resets background funnel and clears settings.
func (h *DashboardHandler) HandleTailscaleDisable(w http.ResponseWriter, r *http.Request) {
	bin := tailscaleBinFn()
	if bin != "" {
		_, _ = tailscaleExec(context.Background(), bin, "funnel", "--bg", "reset")
	}
	_ = h.Repo.UpdateSettingsRaw(map[string]any{
		"tailscaleEnabled": false,
		"tailscaleUrl":     "",
	})
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
	})
}
