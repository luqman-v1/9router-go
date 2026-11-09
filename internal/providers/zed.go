package providers

// Zed Cloud client.
//
// Zed's hosted models are not an OpenAI-compatible endpoint: requests are
// wrapped in a Zed-specific envelope and authenticated in two layers.
//
//   Layer 1 — the account access token, sent as
//     "Authorization: {user_id} {access_token}"
//     It authorizes the account-level calls: /client/users/me and
//     /client/llm_tokens. Zed never sends a bearer token at this layer.
//
//   Layer 2 — a short-lived LLM token, minted by exchanging the access token
//     at POST /client/llm_tokens, sent as
//     "Authorization: Bearer {llm_token}"
//     It authorizes the model calls: /models and /completions.
//
// The account token is the one the OAuth exchange stores; the LLM token is
// minted per account and cached until it expires.
//
// Upstream reference (zed-industries/zed):
//   crates/cloud_api_client/src/cloud_api_client.rs — the "{uid} {token}" header
//   crates/cloud_llm_client/src/cloud_llm_client.rs   — the CompletionBody envelope

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// zedCloudBaseURL is the host serving both the account API and the LLM API.
// It is a variable so tests can point it at a local server.
var zedCloudBaseURL = "https://cloud.zed.dev"

// ZedCloudBaseURL exposes the host so connection tests can reach the account
// API without duplicating the URL.
func ZedCloudBaseURL() string { return zedCloudBaseURL }

// SetZedCloudBaseURL points the client at another host. Tests use it to stand
// a local server in for cloud.zed.dev.
func SetZedCloudBaseURL(url string) { zedCloudBaseURL = url }

const (
	// ZedLLMTokensPath mints the short-lived LLM token.
	ZedLLMTokensPath = "/client/llm_tokens"

	// ZedModelsPath lists the models the account may use.
	ZedModelsPath = "/models"

	// ZedCompletionsPath runs a completion.
	ZedCompletionsPath = "/completions"

	// ZedSystemIDHeader identifies the installing client instance.
	ZedSystemIDHeader = "x-zed-system-id"

	// zedLLMTokenTTL bounds how long a cached LLM token is trusted when its
	// expiry cannot be read. The upstream token is a JWT with an `exp` claim;
	// when the claim is missing or unparseable this conservative window keeps
	// a possibly-dead token from outliving its usefulness.
	zedLLMTokenTTL = 30 * time.Minute

	// zedLLMTokenSkew retires a cached token slightly before its stated expiry
	// so a request never starts with a token that expires mid-flight.
	zedLLMTokenSkew = 60 * time.Second
)

// ZedModel is one entry of Zed's model catalogue.
//
// Zed states far more about a model than a client needs to guess: the exact
// windows, whether thinking can be turned off, which effort levels it accepts,
// and whether it can call tools in parallel. Carrying all of it lets the
// gateway publish the provider's own numbers instead of re-deriving them, so a
// harness never has to hard-code an override.
type ZedModel struct {
	Provider    string `json:"provider"`
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	IsLatest    bool   `json:"is_latest"`

	// MaxTokenCount is the context window; MaxTokenCountInMaxMode replaces it
	// when the client asks for max mode and the model offers one.
	MaxTokenCount          int  `json:"max_token_count"`
	MaxTokenCountInMaxMode *int `json:"max_token_count_in_max_mode"`
	MaxOutputTokens        int  `json:"max_output_tokens"`

	SupportsTools                bool `json:"supports_tools"`
	SupportsImages               bool `json:"supports_images"`
	SupportsThinking             bool `json:"supports_thinking"`
	SupportsDisablingThinking    bool `json:"supports_disabling_thinking"`
	SupportsParallelToolCalls    bool `json:"supports_parallel_tool_calls"`
	SupportsStreamingTools       bool `json:"supports_streaming_tools"`
	SupportsServerSideCompaction bool `json:"supports_server_side_compaction"`
	SupportsMaxMode              bool `json:"supports_max_mode"`
	SupportsFastMode             bool `json:"supports_fast_mode"`
	IsDisabled                   bool `json:"is_disabled"`

	// EffortLevels are the reasoning-effort values the model accepts, in the
	// provider's own order. EffortDefault is the level it picks when the
	// client states none.
	EffortLevels  []string `json:"-"`
	EffortDefault string   `json:"-"`
}

// ZedEffortLevel is one selectable reasoning-effort level.
type ZedEffortLevel struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	IsDefault bool   `json:"is_default"`
}

// zedCatalogResponse is the /models payload.
type zedCatalogResponse struct {
	Models []struct {
		Provider                     string           `json:"provider"`
		ID                           string           `json:"id"`
		DisplayName                  string           `json:"display_name"`
		IsLatest                     bool             `json:"is_latest"`
		MaxTokenCount                int              `json:"max_token_count"`
		MaxTokenCountInMaxMode       *int             `json:"max_token_count_in_max_mode"`
		MaxOutputTokens              int              `json:"max_output_tokens"`
		SupportsTools                bool             `json:"supports_tools"`
		SupportsImages               bool             `json:"supports_images"`
		SupportsThinking             bool             `json:"supports_thinking"`
		SupportsDisablingThinking    bool             `json:"supports_disabling_thinking"`
		SupportsParallelToolCalls    bool             `json:"supports_parallel_tool_calls"`
		SupportsStreamingTools       bool             `json:"supports_streaming_tools"`
		SupportsServerSideCompaction bool             `json:"supports_server_side_compaction"`
		SupportsMaxMode              bool             `json:"supports_max_mode"`
		SupportsFastMode             bool             `json:"supports_fast_mode"`
		IsDisabled                   bool             `json:"is_disabled"`
		SupportedEffortLvls          []ZedEffortLevel `json:"supported_effort_levels"`
	} `json:"models"`
}

// ZedCredentials are the per-connection values the Zed API needs.
type ZedCredentials struct {
	// AccessToken is the stored account token. It is a JSON blob
	// ("{"version":2,"id":"client_token_...","token":"..."}") as Zed issues it,
	// but a bare token string is accepted too.
	AccessToken string
	// UserID identifies the account to the cloud API.
	UserID string
	// OrganizationID selects the org whose models and quota are used.
	OrganizationID string
	// SystemID identifies the client instance.
	SystemID string
}

// zedTokenStore caches minted LLM tokens per credential.
type zedTokenStore struct {
	mu     sync.Mutex
	tokens map[string]zedCachedToken
}

type zedCachedToken struct {
	token     string
	expiresAt time.Time
}

var zedTokens = zedTokenStore{tokens: map[string]zedCachedToken{}}

// zedCredentialKey identifies one credential in the token cache. The access
// token is hashed so the raw credential is never kept in a map key.
func zedCredentialKey(creds ZedCredentials) string {
	sum := sha256.Sum256([]byte(creds.AccessToken))
	// The host is part of the key: a token minted by one deployment must never
	// be handed to another.
	return zedCloudBaseURL + "|" + creds.UserID + "|" + creds.OrganizationID + "|" + hex.EncodeToString(sum[:])
}

// ZedAuthHeader builds the layer-1 Authorization value: "{user_id} {access_token}".
//
// The access token is the credential blob Zed issued, verbatim — including
// the JSON envelope. Zed validates the blob as a whole (it carries the token
// id and version alongside the secret), so sending only its inner "token"
// field is rejected with 401 even though the secret itself is intact.
func ZedAuthHeader(creds ZedCredentials) string {
	return creds.UserID + " " + strings.TrimSpace(creds.AccessToken)
}

// zedLLMTokenResponse is the /client/llm_tokens payload.
type zedLLMTokenResponse struct {
	Token string `json:"token"`
}

// ZedLLMToken returns a valid LLM token for the credential, minting one when
// the cache is cold or the cached token has expired.
func ZedLLMToken(ctx context.Context, client *http.Client, creds ZedCredentials) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	key := zedCredentialKey(creds)

	zedTokens.mu.Lock()
	cached, ok := zedTokens.tokens[key]
	zedTokens.mu.Unlock()
	if ok && time.Now().Before(cached.expiresAt) {
		return cached.token, nil
	}

	token, expiresAt, err := zedMintLLMToken(ctx, client, creds)
	if err != nil {
		return "", err
	}
	zedTokens.mu.Lock()
	zedTokens.tokens[key] = zedCachedToken{token: token, expiresAt: expiresAt}
	zedTokens.mu.Unlock()
	return token, nil
}

// zedMintLLMToken exchanges the account token for a fresh LLM token.
func zedMintLLMToken(ctx context.Context, client *http.Client, creds ZedCredentials) (string, time.Time, error) {
	body := map[string]any{"organization_id": creds.OrganizationID}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("zed: marshal llm token request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, zedCloudBaseURL+ZedLLMTokensPath, strings.NewReader(string(payload)))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("zed: build llm token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", ZedAuthHeader(creds))
	if creds.SystemID != "" {
		req.Header.Set(ZedSystemIDHeader, creds.SystemID)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("zed: llm token request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("zed: read llm token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("zed: llm token rejected with status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed zedLLMTokenResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", time.Time{}, fmt.Errorf("zed: parse llm token response: %w", err)
	}
	if parsed.Token == "" {
		return "", time.Time{}, fmt.Errorf("zed: llm token response carried no token")
	}
	return parsed.Token, zedTokenExpiry(parsed.Token, time.Now()), nil
}

// zedTokenExpiry reads the `exp` claim of the LLM token. A token whose
// payload cannot be read is given the conservative default window.
func zedTokenExpiry(token string, now time.Time) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return now.Add(zedLLMTokenTTL)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return now.Add(zedLLMTokenTTL)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return now.Add(zedLLMTokenTTL)
	}
	expiry := time.Unix(claims.Exp, 0)
	// A token that is already expired (or about to be) is not cached: the
	// next caller mints a fresh one.
	if !expiry.After(now.Add(zedLLMTokenSkew)) {
		return now.Add(zedLLMTokenSkew)
	}
	return expiry
}

// ZedFetchModels returns the models the account may use.
func ZedFetchModels(ctx context.Context, client *http.Client, creds ZedCredentials) ([]ZedModel, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	token, err := ZedLLMToken(ctx, client, creds)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zedCloudBaseURL+ZedModelsPath, nil)
	if err != nil {
		return nil, fmt.Errorf("zed: build models request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if creds.SystemID != "" {
		req.Header.Set(ZedSystemIDHeader, creds.SystemID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zed: models request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("zed: read models response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("zed: models rejected with status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed zedCatalogResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("zed: parse models response: %w", err)
	}
	models := make([]ZedModel, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		levels := make([]string, 0, len(m.SupportedEffortLvls))
		defaultLevel := ""
		for _, lvl := range m.SupportedEffortLvls {
			if lvl.Value == "" {
				continue
			}
			levels = append(levels, lvl.Value)
			if lvl.IsDefault {
				defaultLevel = lvl.Value
			}
		}
		models = append(models, ZedModel{
			Provider:                     m.Provider,
			ID:                           m.ID,
			DisplayName:                  m.DisplayName,
			IsLatest:                     m.IsLatest,
			MaxTokenCount:                m.MaxTokenCount,
			MaxTokenCountInMaxMode:       m.MaxTokenCountInMaxMode,
			MaxOutputTokens:              m.MaxOutputTokens,
			SupportsTools:                m.SupportsTools,
			SupportsImages:               m.SupportsImages,
			SupportsThinking:             m.SupportsThinking,
			SupportsDisablingThinking:    m.SupportsDisablingThinking,
			SupportsParallelToolCalls:    m.SupportsParallelToolCalls,
			SupportsStreamingTools:       m.SupportsStreamingTools,
			SupportsServerSideCompaction: m.SupportsServerSideCompaction,
			SupportsMaxMode:              m.SupportsMaxMode,
			SupportsFastMode:             m.SupportsFastMode,
			IsDisabled:                   m.IsDisabled,
			EffortLevels:                 levels,
			EffortDefault:                defaultLevel,
		})
	}
	return models, nil
}

// ZedWireProvider maps a model id to the provider name Zed's wire protocol
// expects. The catalogue reports these names ("anthropic", "google",
// "open_ai"), and they are also what the model id prefix implies.
func ZedWireProvider(modelID string) string {
	id := strings.ToLower(strings.TrimSpace(modelID))
	switch {
	case strings.HasPrefix(id, "claude"):
		return "anthropic"
	case strings.HasPrefix(id, "gemini"):
		return "google"
	case strings.HasPrefix(id, "gpt"), strings.HasPrefix(id, "o1"), strings.HasPrefix(id, "o3"),
		strings.HasPrefix(id, "o4"):
		return "open_ai"
	}
	return ""
}
