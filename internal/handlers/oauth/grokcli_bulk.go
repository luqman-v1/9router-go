package oauth

import (
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// GrokCliImportItem represents one item in a bulk import payload (supporting snake_case and camelCase).
type GrokCliImportItem struct {
	AccessToken          string         `json:"access_token"`
	AccessTokenCamel     string         `json:"accessToken"`
	RefreshToken         string         `json:"refresh_token"`
	RefreshTokenCamel    string         `json:"refreshToken"`
	IDToken              string         `json:"id_token"`
	IDTokenCamel         string         `json:"idToken"`
	Email                string         `json:"email"`
	ExpiresIn            float64        `json:"expires_in"`
	ExpiresInCamel       float64        `json:"expiresIn"`
	ExpiresAt            string         `json:"expires_at"`
	ExpiresAtCamel       string         `json:"expiresAt"`
	DisplayName          string         `json:"displayName"`
	Name                 string         `json:"name"`
	ProviderSpecificData map[string]any `json:"providerSpecificData"`
}

func (item *GrokCliImportItem) GetAccessToken() string {
	if item.AccessToken != "" {
		return item.AccessToken
	}
	return item.AccessTokenCamel
}

func (item *GrokCliImportItem) GetRefreshToken() string {
	if item.RefreshToken != "" {
		return item.RefreshToken
	}
	return item.RefreshTokenCamel
}

func (item *GrokCliImportItem) GetIDToken() string {
	if item.IDToken != "" {
		return item.IDToken
	}
	return item.IDTokenCamel
}

func (item *GrokCliImportItem) GetExpiresAt() string {
	if item.ExpiresAt != "" {
		return item.ExpiresAt
	}
	if item.ExpiresAtCamel != "" {
		return item.ExpiresAtCamel
	}
	expIn := item.ExpiresIn
	if expIn <= 0 {
		expIn = item.ExpiresInCamel
	}
	if expIn > 0 {
		return time.Now().Add(time.Duration(expIn) * time.Second).UTC().Format(time.RFC3339)
	}
	return ""
}

func (item *GrokCliImportItem) GetName() string {
	if item.DisplayName != "" {
		return item.DisplayName
	}
	return item.Name
}

// parseGrokCliBulkJSON parses flexible JSON inputs (array, wrapped object, or concatenated objects).
func parseGrokCliBulkJSON(raw []byte) ([]map[string]any, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, fmt.Errorf("empty request body")
	}

	// 1. Try direct unmarshal as array
	var arr []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &arr); err == nil {
		return arr, nil
	}

	// 2. Try direct unmarshal as single object
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err == nil {
		if accounts, ok := obj["accounts"].([]any); ok {
			var out []map[string]any
			for _, a := range accounts {
				if m, ok := a.(map[string]any); ok {
					out = append(out, m)
				}
			}
			return out, nil
		}
		if tokens, ok := obj["tokens"].([]any); ok {
			var out []map[string]any
			for _, t := range tokens {
				if m, ok := t.(map[string]any); ok {
					out = append(out, m)
				}
			}
			return out, nil
		}
		return []map[string]any{obj}, nil
	}

	// 3. Try fixing concatenated JSON objects (e.g. `}{` or `}\n{`)
	fixed := trimmed
	if !strings.HasPrefix(fixed, "[") {
		fixed = strings.ReplaceAll(fixed, "}\n{", "},{")
		fixed = strings.ReplaceAll(fixed, "}\r\n{", "},{")
		fixed = strings.ReplaceAll(fixed, "}{", "},{")
		fixed = strings.TrimSuffix(fixed, ",")
		fixed = "[" + fixed + "]"
		if err := json.Unmarshal([]byte(fixed), &arr); err == nil {
			return arr, nil
		}
	}

	return nil, fmt.Errorf("invalid JSON payload: must be object or array")
}

// extractEmailFromJWT attempts to decode unverified JWT claims to extract user email.
func extractEmailFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload := parts[1]
	if l := len(payload) % 4; l > 0 {
		payload += strings.Repeat("=", 4-l)
	}
	data, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		data, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(data, &claims); err != nil {
		return ""
	}
	for _, k := range []string{"email", "unique_name", "preferred_username", "sub"} {
		if v, ok := claims[k].(string); ok && v != "" && strings.Contains(v, "@") {
			return v
		}
	}
	return ""
}

// HandleOAuthGrokCliBulkImport handles bulk Grok CLI OAuth account import.
// POST /api/oauth/grok-cli/bulk-import
func (h *OAuthHandler) HandleOAuthGrokCliBulkImport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	items, err := parseGrokCliBulkJSON(body)
	if err != nil || len(items) == 0 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request payload: %v", err))
		return
	}

	type ImportResult struct {
		Index int    `json:"index"`
		Ok    bool   `json:"ok"`
		ID    string `json:"id,omitempty"`
		Email string `json:"email,omitempty"`
		Error string `json:"error,omitempty"`
	}

	var results []ImportResult
	success := 0
	failed := 0

	for i, raw := range items {
		itemBytes, _ := json.Marshal(raw)
		var item GrokCliImportItem
		if err := json.Unmarshal(itemBytes, &item); err != nil {
			failed++
			results = append(results, ImportResult{Index: i, Ok: false, Error: "item is not a valid object"})
			continue
		}

		accessToken := item.GetAccessToken()
		if accessToken == "" {
			failed++
			results = append(results, ImportResult{Index: i, Ok: false, Error: "missing access_token / accessToken"})
			continue
		}

		refreshToken := item.GetRefreshToken()
		idToken := item.GetIDToken()
		email := item.Email
		if email == "" {
			if idToken != "" {
				email = extractEmailFromJWT(idToken)
			}
			if email == "" {
				email = extractEmailFromJWT(accessToken)
			}
		}

		expiresAt := item.GetExpiresAt()
		name := item.GetName()
		if name == "" {
			if email != "" {
				name = email
			} else {
				name = "Grok CLI Account"
			}
		}

		connID := "grok-cli-oauth-" + randomString(12)
		psd := map[string]any{
			"authMethod": "device_code",
		}
		if idToken != "" {
			psd["idToken"] = idToken
		}
		if email != "" {
			psd["email"] = email
		}
		if item.ProviderSpecificData != nil {
			for k, v := range item.ProviderSpecificData {
				psd[k] = v
			}
		}

		dataMap := map[string]any{
			"accessToken":          accessToken,
			"providerSpecificData": psd,
		}
		if refreshToken != "" {
			dataMap["refreshToken"] = refreshToken
		}
		if expiresAt != "" {
			dataMap["expiresAt"] = expiresAt
		}
		if email != "" {
			dataMap["email"] = email
		}

		dataJSON, err := json.Marshal(dataMap)
		if err != nil {
			failed++
			results = append(results, ImportResult{Index: i, Ok: false, Error: fmt.Sprintf("marshal connection data: %v", err)})
			continue
		}

		now := currentTimestamp()
		_, err = h.Repo.RawDB().Exec(
			`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, 'grok-cli', 'oauth', ?, 1, ?, ?, ?)`,
			connID, name, string(dataJSON), now, now,
		)
		if err != nil {
			log.Error("oauth", "save Grok CLI bulk connection failed", "error", err)
			failed++
			results = append(results, ImportResult{Index: i, Ok: false, Error: err.Error()})
			continue
		}

		success++
		results = append(results, ImportResult{
			Index: i,
			Ok:    true,
			ID:    connID,
			Email: email,
		})
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"total":   len(items),
		"success": success,
		"failed":  failed,
		"results": results,
	})
}
