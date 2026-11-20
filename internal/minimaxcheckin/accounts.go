package minimaxcheckin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

// RepoAccounts builds an account source from the stored provider connections.
// Only OAuth connections qualify: a desktop lease (mcode-lease) stores no token
// here, and magpie's checkin hook requires an oauth sign-in. Credentials stay
// in the database; only the bearer token is copied into the request, and the
// two sites never share one another's credentials.
func RepoAccounts(repo *db.Repo) AccountSource {
	return func(ctx context.Context) ([]Account, error) {
		if repo == nil {
			return nil, nil
		}
		var out []Account
		for _, provider := range []string{"minimax-code", "minimax-code-global"} {
			conns, err := repo.GetProviderConnections(provider, true)
			if err != nil {
				return nil, fmt.Errorf("minimaxcheckin: list %s: %w", provider, err)
			}
			for _, conn := range conns {
				if conn.AuthType != "oauth" {
					continue
				}
				a := AccountFromConnection(provider, conn)
				if a.Token != "" && a.ID != "" {
					out = append(out, a)
				}
			}
		}
		return out, nil
	}
}

// AccountFromConnection builds a check-in account from one stored connection.
// Only OAuth connections carry a token here; a desktop lease stores none.
func AccountFromConnection(provider string, conn *models.ProviderConnection) Account {
	a := Account{Provider: provider, ID: conn.ID, Name: connectionName(conn)}
	var data map[string]any
	if err := json.Unmarshal([]byte(conn.Data), &data); err != nil {
		return a
	}
	a.Token, _ = data["accessToken"].(string)
	a.Token = strings.TrimSpace(a.Token)
	if psd, ok := data["providerSpecificData"].(map[string]any); ok {
		a.UserID, _ = psd["realUserID"].(string)
		a.UserID = strings.TrimSpace(a.UserID)
	}
	return a
}

func connectionName(conn *models.ProviderConnection) string {
	if conn.Name != nil && strings.TrimSpace(*conn.Name) != "" {
		return strings.TrimSpace(*conn.Name)
	}
	if conn.Email != nil && strings.TrimSpace(*conn.Email) != "" {
		return strings.TrimSpace(*conn.Email)
	}
	return conn.ID
}

// ConfigFromRepo reads the minimaxCheckin setting without caching it, so a
// dashboard toggle takes effect on the next tick.
func ConfigFromRepo(repo *db.Repo) func() Config {
	return func() Config {
		if repo == nil {
			return Config{}
		}
		raw, err := repo.GetSettingsRaw()
		if err != nil {
			return Config{}
		}
		enabled, _ := raw["minimaxCheckin"].(bool)
		return Config{Enabled: enabled}
	}
}
