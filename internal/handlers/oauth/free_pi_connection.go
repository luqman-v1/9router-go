package oauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/executor"
)

// saveFreePiConnection persists the connection a successful login produced.
//
// The session id is derived (not minted) by the same function the executor uses,
// so the value written here and the value sent on the wire cannot disagree. See
// executor/free_pi_session.go for why derivation rather than randomness is what
// keeps one account on one upstream session.
func (h *OAuthHandler) saveFreePiConnection(w http.ResponseWriter, r *http.Request, token string) {
	cv, err := freePiFetchClientVersion(r.Context())
	if err != nil {
		// Not fatal: the token is the thing that matters, and the catalogue is
		// re-derivable later.
		log.Warn("oauth", "free-pi client-version unavailable; using the compiled-in floor", "error", err)
	}

	clientVersion := cv.Latest
	if clientVersion == "" {
		clientVersion = providers.FreePiClientVersion
	}

	connID := "fp-" + shortHash(token)
	providerSpecific := map[string]any{
		"authMethod":           "device_flow",
		"freePiSessionId":      executor.FreePiSessionID(token),
		"freePiClientVersion":  clientVersion,
		"freePiConsentVersion": providers.FreePiConsentVersion,
	}
	if models := freePiModelIDs(cv); len(models) > 0 {
		providerSpecific["freePiModels"] = models
	}

	connName := connectionDisplayName(providers.FreePiProviderID, "", "", connID)

	data := map[string]any{
		"apiKey":               token,
		"accessToken":          token,
		"providerSpecificData": providerSpecific,
	}
	dataBytes, err := json.Marshal(data)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to marshal connection data")
		return
	}

	if h.Repo == nil || h.Repo.RawDB() == nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"status": "connected", "connectionId": connID,
		})
		return
	}

	ctx := r.Context()
	now := currentTimestamp()
	var exists int
	if err := h.Repo.RawDB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM providerConnections WHERE id = ?", connID).Scan(&exists); err != nil {
		log.Error("oauth", "free-pi connection lookup failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to check existing connection")
		return
	}

	if exists > 0 {
		_, err = h.Repo.RawDB().ExecContext(ctx,
			"UPDATE providerConnections SET data = ?, updatedAt = ?, isActive = 1 WHERE id = ?",
			string(dataBytes), now, connID)
	} else {
		_, err = h.Repo.RawDB().ExecContext(ctx,
			`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt)
			 VALUES (?, ?, 'oauth', ?, 1, ?, ?, ?)`,
			connID, providers.FreePiProviderID, connName, string(dataBytes), now, now)
	}
	if err != nil {
		log.Error("oauth", "save free-pi connection failed", "conn", connID, "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save connection: %v", err))
		return
	}

	log.Info("oauth", "saved free-pi connection", "conn", connID, "clientVersion", clientVersion)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status":       "connected",
		"connectionId": connID,
	})
}

// freePiConnection loads one connection row.
func (h *OAuthHandler) freePiConnection(connID string) (*models.ProviderConnection, error) {
	if h.Repo == nil {
		return nil, errors.New("no repository configured")
	}
	conn, err := h.Repo.GetProviderConnectionByID(connID)
	if err != nil {
		return nil, fmt.Errorf("load connection %s: %w", connID, err)
	}
	if conn == nil || conn.Provider != providers.FreePiProviderID {
		return nil, fmt.Errorf("no free-pi connection with id %s", connID)
	}
	return conn, nil
}

// freePiConnectionToken reads the bearer credential off a connection row. The
// device flow writes it under both apiKey and accessToken; a manually created
// connection may carry only one.
func freePiConnectionToken(conn *models.ProviderConnection) string {
	if conn == nil {
		return ""
	}
	var data struct {
		APIKey      string `json:"apiKey"`
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal([]byte(conn.Data), &data); err != nil {
		log.Error("oauth", "parse free-pi connection data failed", "conn", conn.ID, "error", err)
		return ""
	}
	if data.APIKey != "" {
		return data.APIKey
	}
	return data.AccessToken
}

// truncateBody keeps upstream prose in an error message without letting a huge
// HTML error page into a log line or an HTTP response.
func truncateBody(raw []byte) string {
	const max = 200
	s := string(bytes.TrimSpace(raw))
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
