package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"9router/proxy/internal/constants"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// getBestConnection retrieves the highest-priority active connection for a provider.
// When connectionID is non-empty, it fetches that specific connection directly.
func (h *ChatHandler) getBestConnection(provider string, connectionID string, excludeIDs []string, model string) (*models.ProviderConnection, *ConnectionData, error) {
	if model != "" && !db.IsProviderHealthy(h.Repo.RawDB(), provider, model) {
		log.Printf("[health] warning: provider %s/%s is unhealthy, proceeding anyway", provider, model)
	}

	var conn *models.ProviderConnection
	var err error

	if connectionID != "" {
		conn, err = h.Repo.GetProviderConnectionByID(connectionID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to fetch connection %s: %w", connectionID, err)
		}
		if conn == nil {
			return nil, nil, fmt.Errorf("connection %s not found", connectionID)
		}
	} else {
		connections, queryErr := h.Repo.GetProviderConnections(provider, true)
		if queryErr != nil {
			return nil, nil, fmt.Errorf("failed to query connections for %s: %w", provider, queryErr)
		}
		if len(connections) == 0 {
			return nil, nil, fmt.Errorf("no active connections for provider: %s", provider)
		}

		excludeSet := make(map[string]bool, len(excludeIDs))
		for _, id := range excludeIDs {
			excludeSet[id] = true
		}

		conn = nil
		for _, c := range connections {
			if !excludeSet[c.ID] {
				conn = c
				break
			}
		}
		if conn == nil {
			return nil, nil, fmt.Errorf("no available connections for provider: %s (all excluded)", provider)
		}
	}

	var connData ConnectionData
	if conn.Data != "" {
		if err := json.Unmarshal([]byte(conn.Data), &connData); err != nil {
			return nil, nil, fmt.Errorf("failed to parse connection data: %w", err)
		}
	}

	return conn, &connData, nil
}

// getProviderConfig returns the upstream configuration for a provider.
func (h *ChatHandler) getProviderConfig(provider string, connData *ConnectionData) (*providers.ProviderConfig, error) {
	if connData.BaseURL != "" {
		return &providers.ProviderConfig{
			BaseURL:    connData.BaseURL,
			AuthHeader: constants.HeaderAuthorization,
			AuthScheme: constants.AuthSchemeBearer,
		}, nil
	}

	if cfg, ok := providers.KnownProviders[provider]; ok {
		return &cfg, nil
	}

	node, nodeData, err := h.Repo.GetProviderNodeByID(provider)
	if err != nil {
		return nil, fmt.Errorf("failed to look up provider node %s: %w", provider, err)
	}
	if node != nil && nodeData != nil && nodeData.BaseURL != "" {
		baseURL := nodeData.BaseURL
		if !strings.HasSuffix(baseURL, "/chat/completions") {
			if strings.HasSuffix(baseURL, "/v1") || strings.HasSuffix(baseURL, "/v1/") {
				baseURL = strings.TrimRight(baseURL, "/") + "/chat/completions"
			} else {
				baseURL = strings.TrimRight(baseURL, "/") + "/v1/chat/completions"
			}
		}
		return &providers.ProviderConfig{
			BaseURL:    baseURL,
			AuthHeader: constants.HeaderAuthorization,
			AuthScheme: constants.AuthSchemeBearer,
		}, nil
	}

	return &providers.ProviderConfig{
		BaseURL:    fmt.Sprintf("https://%s.example.com/v1/chat/completions", provider),
		AuthHeader: constants.HeaderAuthorization,
		AuthScheme: constants.AuthSchemeBearer,
	}, nil
}

// extractAPIKey gets the API key from a connection's data.
func extractAPIKey(connData *ConnectionData) string {
	if connData.APIKey != "" {
		return connData.APIKey
	}
	return connData.AccessToken
}
