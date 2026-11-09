package dashboard

// Catalogue listing for the Zed provider, used by the dashboard's
// "Available models" panel.
//
// Zed's catalogue lives at GET https://cloud.zed.dev/models behind a
// short-lived LLM token, which is minted from the account token stored on
// the connection (see internal/providers/zed.go). The panel therefore cannot
// use the plain bearer fetch the other providers share.

import (
	"context"
	json "encoding/json/v2"
	"fmt"

	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// ZedModel is one entry of Zed's catalogue, in the shape the dashboard panel
// renders. Every field Zed states is carried through, so the panel and any
// client reading it can rely on the provider's own numbers.
type ZedModel struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	ContextLength   int      `json:"contextLength"`
	MaxOutputTokens int      `json:"maxOutputTokens"`
	IsVL            bool     `json:"isVL"`
	IsReasoning     bool     `json:"isReasoning"`
	Description     string   `json:"description"`
	Provider        string   `json:"provider,omitempty"`
	SupportsTools   bool     `json:"supportsTools"`
	CanDisableThink bool     `json:"thinkingCanDisable"`
	ThinkingLevels  []string `json:"thinkingLevels,omitempty"`
	ThinkingDefault string   `json:"thinkingDefault,omitempty"`
	ParallelTools   bool     `json:"parallelToolCalls"`
	StreamingTools  bool     `json:"streamingTools"`
	MaxMode         bool     `json:"maxMode"`
	FastMode        bool     `json:"fastMode"`
	IsLatest        bool     `json:"isLatest"`
}

// zedConnectionData is the part of a connection row the Zed catalogue needs.
type zedConnectionData struct {
	APIKey               string         `json:"apiKey"`
	AccessToken          string         `json:"accessToken"`
	ProviderSpecificData map[string]any `json:"providerSpecificData"`
}

// fetchZedCatalogModels returns the models the account may use.
func fetchZedCatalogModels(ctx context.Context, conn *models.ProviderConnection, connData *zedConnectionData) ([]ZedModel, error) {
	creds := providers.ZedCredentials{
		AccessToken:    zedCatalogToken(conn, connData),
		UserID:         zedCatalogString(connData, "userId"),
		OrganizationID: zedCatalogString(connData, "organizationId"),
		SystemID:       zedCatalogString(connData, "systemId"),
	}
	if creds.AccessToken == "" {
		return nil, fmt.Errorf("zed credential is missing — re-authorize this connection")
	}
	if creds.UserID == "" {
		return nil, fmt.Errorf("zed user id is missing — re-authorize this connection")
	}

	catalog, err := providers.ZedFetchModels(ctx, nil, creds)
	if err != nil {
		return nil, err
	}
	out := make([]ZedModel, 0, len(catalog))
	for _, m := range catalog {
		if m.IsDisabled || m.ID == "" {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		out = append(out, ZedModel{
			ID:              m.ID,
			Name:            name,
			ContextLength:   m.MaxTokenCount,
			MaxOutputTokens: m.MaxOutputTokens,
			IsVL:            m.SupportsImages,
			IsReasoning:     m.SupportsThinking,
			Description:     m.Provider,
			Provider:        m.Provider,
			SupportsTools:   m.SupportsTools,
			CanDisableThink: m.SupportsDisablingThinking,
			ThinkingLevels:  m.EffortLevels,
			ThinkingDefault: m.EffortDefault,
			ParallelTools:   m.SupportsParallelToolCalls,
			StreamingTools:  m.SupportsStreamingTools,
			MaxMode:         m.SupportsMaxMode,
			FastMode:        m.SupportsFastMode,
			IsLatest:        m.IsLatest,
		})
	}
	return out, nil
}

// zedCatalogToken reads the account credential from the connection row, which
// keeps it in the data blob rather than in a dedicated column.
func zedCatalogToken(conn *models.ProviderConnection, connData *zedConnectionData) string {
	if connData != nil {
		if token := connData.AccessToken; token != "" {
			return token
		}
		if token := connData.APIKey; token != "" {
			return token
		}
	}
	if conn == nil {
		return ""
	}
	// The credential lives in the data blob, which the shared struct does not
	// carry for a connection read outside the request path.
	var raw struct {
		AccessToken string `json:"accessToken"`
		APIKey      string `json:"apiKey"`
	}
	if err := json.Unmarshal([]byte(conn.Data), &raw); err != nil {
		return ""
	}
	if raw.AccessToken != "" {
		return raw.AccessToken
	}
	return raw.APIKey
}

// zedCatalogString reads a string field from the connection's
// providerSpecificData block.
func zedCatalogString(connData *zedConnectionData, key string) string {
	if connData == nil || connData.ProviderSpecificData == nil {
		return ""
	}
	value, _ := connData.ProviderSpecificData[key].(string)
	return value
}
