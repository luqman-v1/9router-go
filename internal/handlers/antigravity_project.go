package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"9router/proxy/internal/log"
	"net/http"
	"strings"
	"time"
)

const loadCodeAssistURL = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
const onboardUserURL = "https://cloudcode-pa.googleapis.com/v1internal:onboardUser"

var lcaMetadata = map[string]any{
	"ideType":    9, // ANTIGRAVITY
	"platform":   2, // DARWIN_ARM64
	"pluginType": 2, // GEMINI
}

// fetchAntigravityProjectID attempts to get the project ID for an antigravity token.
func fetchAntigravityProjectID(client *http.Client, accessToken string) string {
	payload, err := json.Marshal(map[string]any{"metadata": lcaMetadata})
	if err != nil {
		log.Error("antigravity", "loadCodeAssist marshal failed", "error", err)
		return ""
	}
	req, err := http.NewRequest("POST", loadCodeAssistURL, bytes.NewReader(payload))
	if err != nil {
		log.Error("antigravity", "loadCodeAssist request failed", "error", err)
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "google-api-nodejs-client/9.15.1")
	req.Header.Set("X-Goog-Api-Client", "google-cloud-sdk vscode_cloudshelleditor/0.1")
	
	clientMetadata, err := json.Marshal(lcaMetadata)
	if err != nil {
		log.Error("antigravity", "marshal metadata failed", "error", err)
		return ""
	}
	req.Header.Set("Client-Metadata", string(clientMetadata))

	resp, err := client.Do(req)
	if err != nil {
		log.Error("antigravity", "loadCodeAssist HTTP error", "error", err)
		return ""
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error("antigravity", "loadCodeAssist read failed", "error", err)
		return ""
	}
	if resp.StatusCode != http.StatusOK {
		log.Warn("antigravity", "loadCodeAssist returned", "status", resp.StatusCode)
	}

	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		log.Error("antigravity", "unmarshal error", "error", err)
		return ""
	}

	pid := extractProjectID(data["cloudaicompanionProject"])
	if pid != "" {
		return pid
	}

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

	return onboardAntigravityUser(client, accessToken, tierID)
}

func onboardAntigravityUser(client *http.Client, accessToken, tierID string) string {
	for attempt := 0; attempt < 3; attempt++ {
		payload, err := json.Marshal(map[string]any{
			"tierId":   tierID,
			"metadata": lcaMetadata,
		})
		if err != nil {
			log.Error("antigravity", "onboardUser marshal failed", "error", err)
			time.Sleep(2 * time.Second)
			continue
		}
		req, err := http.NewRequest("POST", onboardUserURL, bytes.NewReader(payload))
		if err != nil {
			log.Error("antigravity", "onboardUser request failed", "error", err)
			time.Sleep(2 * time.Second)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("User-Agent", "google-api-nodejs-client/9.15.1")
		req.Header.Set("X-Goog-Api-Client", "google-cloud-sdk vscode_cloudshelleditor/0.1")
		
		clientMetadata, err := json.Marshal(lcaMetadata)
		if err != nil {
			log.Error("antigravity", "marshal metadata failed", "error", err)
			time.Sleep(2 * time.Second)
			continue
		}
		req.Header.Set("Client-Metadata", string(clientMetadata))

		resp, err := client.Do(req)
		if err != nil {
			log.Error("antigravity", "onboardUser HTTP error", "attempt", attempt, "error", err)
			time.Sleep(2 * time.Second)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Error("antigravity", "onboardUser read failed", "error", err)
			time.Sleep(2 * time.Second)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			log.Warn("antigravity", "onboardUser returned", "status", resp.StatusCode)
		}

		var data map[string]any
		if err := json.Unmarshal(body, &data); err != nil {
			log.Error("antigravity", "onboardUser unmarshal failed", "error", err)
			time.Sleep(2 * time.Second)
			continue
		}

		if done, _ := data["done"].(bool); done {
			if respMap, ok := data["response"].(map[string]any); ok {
				pid := extractProjectID(respMap["cloudaicompanionProject"])
				if pid != "" {
					return pid
				}
			}
			return ""
		}
		time.Sleep(2 * time.Second)
	}
	return ""
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
