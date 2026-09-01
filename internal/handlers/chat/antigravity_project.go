package chat

import (
	"9router/proxy/internal/log"
	"bytes"
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// vars (not consts) so tests can point them at a local server.
// Discovery/onboarding RPCs stay on PROD: the daily host rejects these calls
// (parity with the Next.js reference — see registry/antigravity.js usage config).
var loadCodeAssistURL = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
var onboardUserURL = "https://cloudcode-pa.googleapis.com/v1internal:onboardUser"

var lcaMetadata = map[string]any{
	"ideType":    9, // ANTIGRAVITY
	"platform":   2, // DARWIN_ARM64
	"pluginType": 2, // GEMINI
}

// projectNoCache short-circuits re-probing the onboarding RPCs for a token whose
// project Google has already confirmed missing (unprovisioned Antigravity/GCP
// account). Without it, every retried request re-hits loadCodeAssist+onboardUser,
// ramming the rate limit — the source of the repeated 429s.
var projectNoCache sync.Map // connID -> unix expiry

const projectNoCacheTTL = 10 * time.Minute

// antigravityProbeDelay is the base backoff between onboarding retries (a var
// so tests can shorten it). The actual delay doubles per attempt (2s, 4s, ...)
// so we do not hammer Google's RPCs during a 429 burst.
var antigravityProbeDelay = 2 * time.Second

// probeBackoffWait sleeps an exponential backoff for the given retry attempt
// (attempt 1 = base, attempt 2 = 2x base, ...) and returns false if ctx was
// cancelled mid-wait so callers can bail out promptly instead of sleeping blind.
func probeBackoffWait(ctx context.Context, attempt int) bool {
	if attempt <= 0 {
		return true
	}
	delay := antigravityProbeDelay * time.Duration(1<<uint(attempt-1))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func projectProbeCached(connID string) bool {
	if connID == "" {
		return false
	}
	v, ok := projectNoCache.Load(connID)
	if !ok {
		return false
	}
	exp, ok := v.(int64)
	return ok && time.Now().Unix() < exp
}

func cacheProjectMissing(connID string) {
	if connID == "" {
		return
	}
	if _, ok := projectNoCache.Load(connID); ok {
		return // already known — don't repeat the guidance on every request
	}
	projectNoCache.Store(connID, time.Now().Add(projectNoCacheTTL).Unix())
	log.Warn("antigravity", "no Antigravity project for this connection — onboard the account once via Antigravity IDE/CLI (antigravity.google), then re-login this connection", "conn", connID)
}

// fetchAntigravityProjectID probes Google's onboarding RPCs for a projectID.
//
// Returns:
//   - pid:         the project ID when one exists
//   - authFailed:  true only when upstream rejected the token (401/403) — a
//     refresh *can* fix this. Anything else (429, 5xx, empty project, network)
//     must NOT trigger redundant refreshes.
//   - noProject:   Google definitively said "no project for this token" (200
//     with nothing mapped). Safe to cache so we stop hammering the RPCs.
func fetchAntigravityProjectID(ctx context.Context, client *http.Client, accessToken string) (pid string, authFailed, noProject bool) {
	payload, err := json.Marshal(map[string]any{"metadata": lcaMetadata})
	if err != nil {
		log.Error("antigravity", "loadCodeAssist marshal failed", "error", err)
		return "", false, false
	}
	req, err := http.NewRequestWithContext(ctx, "POST", loadCodeAssistURL, bytes.NewReader(payload))
	if err != nil {
		log.Error("antigravity", "loadCodeAssist request failed", "error", err)
		return "", false, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "google-api-nodejs-client/9.15.1")
	req.Header.Set("X-Goog-Api-Client", "google-cloud-sdk vscode_cloudshelleditor/0.1")

	clientMetadata, err := json.Marshal(lcaMetadata)
	if err != nil {
		log.Error("antigravity", "marshal metadata failed", "error", err)
		return "", false, false
	}
	req.Header.Set("Client-Metadata", string(clientMetadata))

	resp, err := client.Do(req)
	if err != nil {
		log.Error("antigravity", "loadCodeAssist HTTP error", "error", err)
		return "", false, false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error("antigravity", "loadCodeAssist read failed", "error", err)
		return "", false, false
	}
	if resp.StatusCode != http.StatusOK {
		log.Warn("antigravity", "loadCodeAssist returned", "status", resp.StatusCode)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return "", true, false
		}
		return "", false, false // transient (429/5xx): DO NOT cache as "no project"
	}

	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		log.Error("antigravity", "unmarshal error", "error", err)
		return "", false, false
	}

	pid = extractProjectID(data["cloudaicompanionProject"])
	if pid != "" {
		return pid, false, false
	}

	// No project from loadCodeAssist — default tier exists, so this token has
	// none mapped yet (provisioning issue, not transient).
	noProject = true

	// Try onboard user
	tierID := "legacy-tier"
	if allowed, ok := data["allowedTiers"].([]any); ok {
		for _, t := range allowed {
			if tm, ok := t.(map[string]any); ok {
				if isDef, _ := tm["isDefault"].(bool); isDef {
					if id, _ := tm["id"].(string); id != "" {
						tierID = strings.TrimSpace(id)
						break
					}
				}
			}
		}
	}

	pid, authFailed, onboardNoProject := onboardAntigravityUser(ctx, client, accessToken, tierID)
	return pid, authFailed, noProject || onboardNoProject
}

func onboardAntigravityUser(ctx context.Context, client *http.Client, accessToken, tierID string) (pid string, authFailed, noProject bool) {
	for attempt := 0; attempt < 3; attempt++ {
		payload, err := json.Marshal(map[string]any{
			"tierId":   tierID,
			"metadata": lcaMetadata,
		})
		if err != nil {
			log.Error("antigravity", "onboardUser marshal failed", "error", err)
			if !probeBackoffWait(ctx, attempt) {
				return "", false, false
			}
			continue
		}
		req, err := http.NewRequestWithContext(ctx, "POST", onboardUserURL, bytes.NewReader(payload))
		if err != nil {
			log.Error("antigravity", "onboardUser request failed", "error", err)
			if !probeBackoffWait(ctx, attempt) {
				return "", false, false
			}
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("User-Agent", "google-api-nodejs-client/9.15.1")
		req.Header.Set("X-Goog-Api-Client", "google-cloud-sdk vscode_cloudshelleditor/0.1")

		clientMetadata, err := json.Marshal(lcaMetadata)
		if err != nil {
			log.Error("antigravity", "marshal metadata failed", "error", err)
			if !probeBackoffWait(ctx, attempt) {
				return "", false, false
			}
			continue
		}
		req.Header.Set("Client-Metadata", string(clientMetadata))

		resp, err := client.Do(req)
		if err != nil {
			log.Error("antigravity", "onboardUser HTTP error", "attempt", attempt, "error", err)
			if !probeBackoffWait(ctx, attempt) {
				return "", false, false
			}
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Error("antigravity", "onboardUser read failed", "error", err)
			if !probeBackoffWait(ctx, attempt) {
				return "", false, false
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			log.Warn("antigravity", "onboardUser returned", "status", resp.StatusCode)
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				return "", true, false
			}
			if !probeBackoffWait(ctx, attempt) {
				return "", false, false
			}
			continue
		}

		var data map[string]any
		if err := json.Unmarshal(body, &data); err != nil {
			log.Error("antigravity", "onboardUser unmarshal failed", "error", err)
			if !probeBackoffWait(ctx, attempt) {
				return "", false, false
			}
			continue
		}

		if done, _ := data["done"].(bool); done {
			if respMap, ok := data["response"].(map[string]any); ok {
				pid = extractProjectID(respMap["cloudaicompanionProject"])
				if pid != "" {
					return pid, false, false
				}
			}
			return "", false, true
		}
		if !probeBackoffWait(ctx, attempt) {
			return "", false, false
		}
	}
	return "", false, false
}

func extractProjectID(val any) string {
	if val == nil {
		return ""
	}
	if s, ok := val.(string); ok {
		return strings.TrimSpace(s)
	}
	if m, ok := val.(map[string]any); ok {
		if id, _ := m["id"].(string); id != "" {
			return strings.TrimSpace(id)
		}
	}
	return ""
}
