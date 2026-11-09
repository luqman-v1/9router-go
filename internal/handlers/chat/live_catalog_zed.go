package chat

// Live catalogue resolution for the Zed provider.
//
// Zed publishes the models an account may use at GET https://cloud.zed.dev/models,
// behind a short-lived LLM token minted from the account token (see
// internal/providers/zed.go). The catalogue replaces the static registry for a
// Zed connection, so the model picker shows exactly what the account can run.

import (
	"context"

	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// fetchZedLiveCatalog returns the models Zed reports for this account.
func (h *ChatHandler) fetchZedLiveCatalog(ctx context.Context, conn *models.ProviderConnection, connData *shared.ConnectionData, accessToken string) []LiveModel {
	creds := providers.ZedCredentials{
		AccessToken:    accessToken,
		UserID:         zedConnectionString(connData, "userId"),
		OrganizationID: zedConnectionString(connData, "organizationId"),
		SystemID:       zedConnectionString(connData, "systemId"),
	}
	if creds.AccessToken == "" || creds.UserID == "" {
		return nil
	}

	reqCtx, cancel := context.WithTimeout(ctx, liveCatalogFastHTTP)
	defer cancel()
	catalog, err := providers.ZedFetchModels(reqCtx, h.Client, creds)
	if err != nil {
		log.Warn("models", "zed catalog fetch failed", "conn", conn.ID, "error", err)
		return nil
	}

	live := make([]LiveModel, 0, len(catalog))
	for _, m := range catalog {
		if m.IsDisabled || m.ID == "" {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		// Publish everything Zed states, so a client never has to override a
		// window, an effort level, or a capability the provider already
		// described.
		live = append(live, LiveModel{
			ID:            m.ID,
			Name:          name,
			ContextLength: m.MaxTokenCount,
			MaxOutput:     m.MaxOutputTokens,
			Capabilities: &LiveCapabilities{
				Thinking:           m.SupportsThinking,
				Agentic:            m.SupportsTools,
				Vision:             m.SupportsImages,
				ContextWindow:      m.MaxTokenCount,
				MaxOutput:          m.MaxOutputTokens,
				ThinkingLevels:     m.EffortLevels,
				ThinkingDefault:    m.EffortDefault,
				ThinkingCanDisable: m.SupportsDisablingThinking,
			},
			LimitsAuthoritative: m.MaxTokenCount > 0 || m.MaxOutputTokens > 0,
		})
	}
	return live
}

// zedConnectionString reads a string field from the connection's
// providerSpecificData block.
func zedConnectionString(connData *shared.ConnectionData, key string) string {
	if connData == nil || connData.ProviderSpecificData == nil {
		return ""
	}
	value, _ := connData.ProviderSpecificData[key].(string)
	return value
}
