package oauth

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/proxy/oauth"
)

// ---------- kiro: method import endpoints (upstream KiroAuthModal parity) ----------
//
// Upstream KiroAuthModal offers: builder-id (device), IDC (device with
// startUrl/region), API key (headless), social Google/GitHub (manual
// callback), import refresh token, import CLIProxyAPI JSON, and auto-import
// from the local AWS SSO cache. The Go dashboard previously only wired the
// bare device flow, so :20129 never matched :20128.

// HandleKiroImport validates a pasted Kiro IDE refresh token and stores it.
// POST /api/oauth/kiro/import {"refreshToken","clientId?","clientSecret?","region?","authMethod?","profileArn?"}
// Upstream parity (import/route.js): IDC tokens refresh via the regional OIDC
// endpoint with client credentials; social/builder-id tokens via the desktop
// social refresh endpoint.
func (h *OAuthHandler) HandleKiroImport(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		RefreshToken string `json:"refreshToken"`
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
		Region       string `json:"region"`
		AuthMethod   string `json:"authMethod"`
		ProfileArn   string `json:"profileArn"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	refreshToken := strings.TrimSpace(req.RefreshToken)
	if refreshToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Refresh token is required")
		return
	}

	region := strings.TrimSpace(req.Region)
	if region == "" {
		region = "us-east-1"
	}
	isIDC := strings.TrimSpace(req.ClientID) != "" && strings.TrimSpace(req.ClientSecret) != ""

	psd := map[string]string{}
	if isIDC {
		psd["clientId"] = strings.TrimSpace(req.ClientID)
		psd["clientSecret"] = strings.TrimSpace(req.ClientSecret)
		psd["region"] = region
	}

	result, err := oauth.Refresh(r.Context(), &oauth.Params{
		Client:               &http.Client{Timeout: 15 * time.Second},
		Provider:             "kiro",
		RefreshToken:         refreshToken,
		ProviderSpecificData: psd,
	})
	if err != nil || result == nil || result.AccessToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("token validation failed: %v", err))
		return
	}

	email := extractEmailFromJWT(result.AccessToken)
	resolvedAuthMethod := "imported"
	providerLabel := "Imported"
	if isIDC {
		resolvedAuthMethod = "idc"
		providerLabel = "Enterprise"
	}
	if am := strings.TrimSpace(req.AuthMethod); am != "" && am != "idc" && !isIDC {
		resolvedAuthMethod = am
	}
	connID := "kiro-" + shortHash(result.AccessToken)
	name := "Kiro"
	if email != "" {
		name += " (" + email + ")"
	}
	expiresIn := result.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	psdBlob := map[string]any{
		"profileArn": firstNonEmpty(strings.TrimSpace(req.ProfileArn), ""),
		"authMethod": resolvedAuthMethod,
		"provider":   providerLabel,
	}
	if isIDC {
		psdBlob["clientId"] = strings.TrimSpace(req.ClientID)
		psdBlob["clientSecret"] = strings.TrimSpace(req.ClientSecret)
		psdBlob["region"] = region
	}
	dataMap := map[string]any{
		"apiKey":               result.AccessToken,
		"accessToken":          result.AccessToken,
		"refreshToken":         firstNonEmpty(result.RefreshToken, refreshToken),
		"expiresAt":            time.Now().Add(time.Duration(expiresIn) * time.Second).UTC().Format(time.RFC3339),
		"providerSpecificData": psdBlob,
	}
	if email != "" {
		dataMap["email"] = email
	}
	h.saveSpecialConnection(w, "kiro", connID, name, email, dataMap, nil)
}

// HandleKiroImportCliProxy imports Kiro CLIProxyAPI external_idp auth JSON.
// POST /api/oauth/kiro/import-cli-proxy {"json"|"auth"|"cliProxyAuth"|...}
// Upstream parity (import-cli-proxy/route.js): normalize via the shared
// external_idp helper, then store with its providerSpecificData.
func (h *OAuthHandler) HandleKiroImportCliProxy(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	payload := body
	for _, k := range []string{"cliProxyAuth", "auth", "json"} {
		if inner, ok := body[k].(map[string]any); ok && len(inner) > 0 {
			payload = inner
			break
		}
		if s, ok := body[k].(string); ok && strings.TrimSpace(s) != "" {
			var inner map[string]any
			if jerr := json.Unmarshal([]byte(s), &inner); jerr == nil && len(inner) > 0 {
				payload = inner
				break
			}
		}
	}

	tokenData, err := normalizeKiroExternalIDPAuth(payload)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	connID := "kiro-" + shortHash(tokenData.AccessToken)
	name := "Kiro"
	if tokenData.Email != "" {
		name += " (" + tokenData.Email + ")"
	}
	dataMap := map[string]any{
		"apiKey":               tokenData.AccessToken,
		"accessToken":          tokenData.AccessToken,
		"refreshToken":         tokenData.RefreshToken,
		"expiresAt":            tokenData.ExpiresAt,
		"providerSpecificData": tokenData.ProviderSpecificData,
	}
	if tokenData.Email != "" {
		dataMap["email"] = tokenData.Email
	}
	h.saveSpecialConnection(w, "kiro", connID, name, tokenData.Email, dataMap, nil)
}

// HandleKiroAutoImport reads the local AWS SSO cache (server host only).
// GET /api/oauth/kiro/auto-import
// Upstream parity (auto-import/route.js): prefer kiro-auth-token.json, scan
// all cache files for a "aorAAAAAG" refresh token, resolve IDC client
// credentials via clientIdHash, and read profileArn from the Kiro profile.
func (h *OAuthHandler) HandleKiroAutoImport(w http.ResponseWriter, r *http.Request) {
	refreshToken, tokenData, foundFile := findKiroCachedRefreshToken()
	if refreshToken == "" {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"found": false,
			"error": "Kiro token not found in AWS SSO cache. Please login to Kiro IDE first.",
		})
		return
	}

	var clientID, clientSecret string
	region, _ := tokenData["region"].(string)
	authMethod, _ := tokenData["authMethod"].(string)
	if hash, _ := tokenData["clientIdHash"].(string); hash != "" {
		if reg := readKiroClientRegistration(hash); reg != nil {
			clientID, _ = reg["clientId"].(string)
			clientSecret, _ = reg["clientSecret"].(string)
		}
	}
	profileArn := readKiroProfileArn()

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"found":        true,
		"refreshToken": refreshToken,
		"source":       foundFile,
		"clientId":     nullableStr(clientID),
		"clientSecret": nullableStr(clientSecret),
		"region":       nullableStr(region),
		"authMethod":   nullableStr(authMethod),
		"profileArn":   nullableStr(normalizeKiroProfileArn(profileArn)),
	})
}

// HandleKiroAPIKey validates a headless Kiro/CodeWhisperer API key and stores it.
// POST /api/oauth/kiro/api-key {"apiKey","region?"}
// Upstream parity (api-key/route.js): validate against the Amazon Q model
// catalog, store with authType api_key and a long horizon expiry.
func (h *OAuthHandler) HandleKiroAPIKey(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		APIKey string `json:"apiKey"`
		Region string `json:"region"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "API key is required")
		return
	}
	region := strings.TrimSpace(req.Region)
	if region == "" {
		region = "us-east-1"
	}

	if err := validateKiroAPIKey(r.Context(), apiKey, region); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "API key validation failed")
		return
	}

	email := extractEmailFromJWT(apiKey)
	name := "Kiro"
	if email != "" {
		name += " (" + email + ")"
	}
	connID := "kiro-" + shortHash(apiKey)
	dataMap := map[string]any{
		"apiKey":      apiKey,
		"accessToken": apiKey,
		"expiresAt":   time.Now().Add(365 * 24 * time.Hour).UTC().Format(time.RFC3339),
		"providerSpecificData": map[string]any{
			"region":     region,
			"authMethod": "api_key",
			"provider":   "API Key",
		},
	}
	if email != "" {
		dataMap["email"] = email
	}
	// api_key connections ride authType api_key (upstream parity: the
	// overview grid counts oauth + apikey/api_key together for kiro).
	h.saveKiroAPIKeyConnection(w, connID, name, email, dataMap)
}

// saveKiroAPIKeyConnection stores an api_key connection (authType differs
// from the oauth default in saveSpecialConnection).
func (h *OAuthHandler) saveKiroAPIKeyConnection(w http.ResponseWriter, connID, name, email string, dataMap map[string]any) {
	now := currentTimestamp()
	dataBytes, err := json.Marshal(dataMap)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to marshal connection data")
		return
	}
	if h.Repo != nil && h.Repo.RawDB() != nil {
		var existing string
		err := h.Repo.RawDB().QueryRow("SELECT data FROM providerConnections WHERE id = ?", connID).Scan(&existing)
		if err == nil && existing != "" {
			_, err = h.Repo.RawDB().Exec(
				"UPDATE providerConnections SET name = ?, data = ?, updatedAt = ? WHERE id = ?", name, string(dataBytes), now, connID)
		} else {
			_, err = h.Repo.RawDB().Exec(
				"INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, ?, 'api_key', ?, 1, ?, ?, ?)",
				connID, "kiro", name, string(dataBytes), now, now)
		}
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save connection: %v", err))
			return
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true, "status": "authorized", "id": connID, "connectionId": connID,
		"provider": "kiro", "name": name, "email": email,
		"connection": map[string]any{"id": connID, "provider": "kiro", "email": email},
	})
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func kiroSSOCacheDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".aws", "sso", "cache")
}

// findKiroCachedRefreshToken scans the AWS SSO cache for a Kiro refresh token.
func findKiroCachedRefreshToken() (string, map[string]any, string) {
	entries, err := os.ReadDir(kiroSSOCacheDir())
	if err != nil {
		return "", nil, ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	ordered := []string{}
	for _, n := range names {
		if n == "kiro-auth-token.json" {
			ordered = append(ordered, n)
		}
	}
	for _, n := range names {
		if n != "kiro-auth-token.json" {
			ordered = append(ordered, n)
		}
	}
	for _, n := range ordered {
		content, err := os.ReadFile(filepath.Join(kiroSSOCacheDir(), n))
		if err != nil {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal(content, &data); err != nil {
			continue
		}
		if rt, _ := data["refreshToken"].(string); strings.HasPrefix(rt, "aorAAAAAG") {
			return rt, data, n
		}
	}
	return "", nil, ""
}

func readKiroClientRegistration(clientIDHash string) map[string]any {
	if clientIDHash == "" {
		return nil
	}
	content, err := os.ReadFile(filepath.Join(kiroSSOCacheDir(), clientIDHash+".json"))
	if err != nil {
		return nil
	}
	var data map[string]any
	if err := json.Unmarshal(content, &data); err != nil {
		return nil
	}
	if cid, _ := data["clientId"].(string); cid == "" {
		return nil
	}
	if cs, _ := data["clientSecret"].(string); cs == "" {
		return nil
	}
	return data
}

func kiroProfilePaths() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return []string{filepath.Join(appData, "Kiro", "User", "globalStorage", "kiro.kiroagent", "profile.json")}
	case "darwin":
		return []string{filepath.Join(home, "Library", "Application Support", "Kiro", "User", "globalStorage", "kiro.kiroagent", "profile.json")}
	default:
		return []string{filepath.Join(home, ".config", "Kiro", "User", "globalStorage", "kiro.kiroagent", "profile.json")}
	}
}

func readKiroProfileArn() string {
	for _, p := range kiroProfilePaths() {
		content, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal(content, &data); err != nil {
			continue
		}
		if arn, _ := data["arn"].(string); arn != "" {
			return arn
		}
	}
	return ""
}

// normalizeKiroProfileArn pins the region segment to us-east-1 (upstream
// parity: the runtime gateway requires us-east-1 in the ARN regardless of
// the IDC region).
func normalizeKiroProfileArn(arn string) string {
	if arn == "" {
		return ""
	}
	parts := strings.Split(arn, ":")
	if len(parts) > 3 && parts[2] == "codewhisperer" {
		parts[3] = "us-east-1"
		return strings.Join(parts, ":")
	}
	return arn
}

// validateKiroAPIKey probes the Amazon Q model catalog with the key.
func validateKiroAPIKey(ctx context.Context, apiKey, region string) error {
	body, _ := json.Marshal(map[string]any{"origin": "AI_EDITOR"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://codewhisperer.us-east-1.amazonaws.com/", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("x-amz-target", "AmazonCodeWhispererService.ListAvailableModels")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("validation returned %d", resp.StatusCode)
	}
	return nil
}
