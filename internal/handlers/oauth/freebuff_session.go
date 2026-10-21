package oauth

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/proxy/executor"
)

var freebuffAPIBaseURL = "https://www.codebuff.com"

// freebuffSessionResponse builds a session status payload, carrying the account
// it describes so the dashboard can name the connection instead of reporting a
// status that belongs to whichever account happened to be first.
func freebuffSessionResponse(conn *models.ProviderConnection, fields map[string]any) map[string]any {
	resp := map[string]any{}
	if conn != nil {
		resp["connectionId"] = conn.ID
		if conn.Name != nil && *conn.Name != "" {
			resp["connectionName"] = *conn.Name
		}
	}
	for k, v := range fields {
		resp[k] = v
	}
	return resp
}

// freebuffConnectionToken reads the bearer token stored on a Freebuff
// connection. Connections written by the device flow carry the same value under
// `authToken`, `accessToken` and `apiKey`; manually imported ones may hold only
// one of them.
func freebuffConnectionToken(conn *models.ProviderConnection) string {
	if conn == nil {
		return ""
	}

	var data struct {
		AuthToken   string `json:"authToken"`
		AccessToken string `json:"accessToken"`
		APIKey      string `json:"apiKey"`
	}
	if err := json.Unmarshal([]byte(conn.Data), &data); err != nil {
		log.Error("oauth", "failed to unmarshal connection data", "conn", conn.ID, "error", err)
		return ""
	}

	if data.AuthToken != "" {
		return data.AuthToken
	}
	if data.AccessToken != "" {
		return data.AccessToken
	}
	return data.APIKey
}

// freebuffConnection resolves the connection a dashboard Freebuff action
// targets: by id when given, otherwise the first active Freebuff connection.
func (h *OAuthHandler) freebuffConnection(connectionID string) (*models.ProviderConnection, error) {
	if h.Repo == nil {
		return nil, nil
	}
	if connectionID != "" {
		return h.Repo.GetProviderConnectionByID(connectionID)
	}

	conns, err := h.Repo.GetProviderConnections("freebuff", true)
	if err != nil {
		return nil, err
	}
	if len(conns) == 0 {
		return nil, nil
	}
	return conns[0], nil
}

// HandleFreebuffSessionStatus returns current active Freebuff session status for a connection.
// GET /api/oauth/freebuff/session
func (h *OAuthHandler) HandleFreebuffSessionStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		handlerutil.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	connectionID := r.URL.Query().Get("connectionId")
	var conn *models.ProviderConnection

	if connectionID != "" {
		if h.Repo != nil {
			var err error
			conn, err = h.Repo.GetProviderConnectionByID(connectionID)
			if err != nil {
				log.Error("oauth", "failed to query connection by id", "conn", connectionID, "error", err)
				handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to query connection")
				return
			}
		}
	} else {
		if h.Repo != nil {
			conns, err := h.Repo.GetProviderConnections("freebuff", true)
			if err != nil {
				log.Error("oauth", "failed to query freebuff connections", "error", err)
				handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to query connections")
				return
			}
			if len(conns) > 0 {
				conn = conns[0]
			}
		}
	}

	if conn == nil {
		handlerutil.WriteJSON(w, http.StatusOK, freebuffSessionResponse(nil, map[string]any{
			"status": "none",
		}))
		return
	}

	authToken := freebuffConnectionToken(conn)

	if authToken == "" {
		handlerutil.WriteJSON(w, http.StatusOK, freebuffSessionResponse(conn, map[string]any{
			"status": "none",
		}))
		return
	}

	reqURL := freebuffAPIBaseURL + "/api/v1/freebuff/session"
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, reqURL, nil)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("create session request failed: %v", err))
		return
	}
	upReq.Header.Set("Authorization", "Bearer "+authToken)
	upReq.Header.Set("User-Agent", constants.FreebuffClientUserAgent)
	upReq.Header.Set("Accept", "application/json")

	resp, err := executor.DoFreebuffHTTP(r.Context(), nil, upReq)
	if err != nil {
		log.Error("oauth", "freebuff session request failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("freebuff session request failed: %v", err))
		return
	}
	defer resp.Body.Close()

	// A refused credential is not one condition. `banned` comes back as
	// 403 {"status":"banned"}, and it means add another account rather than run
	// the login flow again — reporting both as `unauthorized` hides that.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		refused := "unauthorized"
		refusalBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		var refusal struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(refusalBody), &refusal); err == nil {
			switch strings.ToLower(strings.TrimSpace(refusal.Status)) {
			case "banned":
				refused = "banned"
			case "country_blocked":
				refused = "country_blocked"
			}
		}
		updateConnectionFreebuffModel(h.Repo, conn, "")
		handlerutil.WriteJSON(w, http.StatusOK, freebuffSessionResponse(conn, map[string]any{
			"status": refused,
		}))
		return
	}
	if resp.StatusCode == http.StatusNotFound {
		handlerutil.WriteJSON(w, http.StatusOK, freebuffSessionResponse(conn, map[string]any{
			"status": "none",
		}))
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to read freebuff session response")
		return
	}

	if resp.StatusCode != http.StatusOK {
		log.Warn("oauth", "freebuff session non-200 response", "status", resp.StatusCode, "bytes", len(body))
		handlerutil.WriteJSON(w, http.StatusOK, freebuffSessionResponse(conn, map[string]any{
			"status": "none",
		}))
		return
	}

	var upstream struct {
		Status             string         `json:"status"`
		CurrentModel       string         `json:"currentModel"`
		Model              string         `json:"model"`
		InstanceID         string         `json:"instanceId"`
		ExpiresAt          string         `json:"expiresAt"`
		AccessTier         string         `json:"accessTier"`
		CountryCode        string         `json:"countryCode"`
		CountryBlockReason string         `json:"countryBlockReason"`
		Freebucks          map[string]any `json:"freebucks"`
		RateLimit          struct {
			Model       string `json:"model"`
			Limit       int    `json:"limit"`
			RecentCount int    `json:"recentCount"`
			PoolLabel   string `json:"poolLabel"`
			ResetAt     string `json:"resetAt"`
			ResetTZ     string `json:"resetTimeZone"`
		} `json:"rateLimit"`
	}
	if err := json.Unmarshal(body, &upstream); err != nil {
		log.Error("oauth", "freebuff session unmarshal failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to parse freebuff session response")
		return
	}

	currentModel := upstream.CurrentModel
	if currentModel == "" {
		currentModel = upstream.Model
	}

	status := strings.ToLower(strings.TrimSpace(upstream.Status))
	if status == "" {
		if currentModel != "" || upstream.InstanceID != "" {
			status = "active"
		} else {
			status = "none"
		}
	}

	// GET answers with a small set of states. Anything else is reported as "no
	// session" rather than leaking an upstream word the dashboard cannot render.
	switch status {
	case "active", "queued", "ended", "unauthorized", "banned", "country_blocked":
	default:
		status = "none"
	}

	respMap := map[string]any{
		"status": status,
	}
	// The seat's identity only means something while one is held: an ended or
	// absent session keeps its last model upstream, and echoing that would name a
	// model the account is no longer on.
	if status == "active" || status == "queued" {
		if currentModel != "" {
			respMap["currentModel"] = currentModel
		}
		if upstream.InstanceID != "" {
			respMap["instanceId"] = upstream.InstanceID
		}
		if upstream.ExpiresAt != "" {
			respMap["expiresAt"] = upstream.ExpiresAt
		}
	}
	if upstream.Freebucks != nil {
		respMap["freebucks"] = upstream.Freebucks
	}
	if upstream.AccessTier != "" {
		respMap["accessTier"] = upstream.AccessTier
	}
	if upstream.CountryCode != "" {
		respMap["countryCode"] = upstream.CountryCode
	}
	if upstream.CountryBlockReason != "" {
		respMap["countryBlockReason"] = upstream.CountryBlockReason
	}
	if upstream.RateLimit.Limit > 0 {
		rateLimit := map[string]any{
			"limit":       upstream.RateLimit.Limit,
			"recentCount": upstream.RateLimit.RecentCount,
		}
		if upstream.RateLimit.Model != "" {
			rateLimit["model"] = upstream.RateLimit.Model
		}
		if upstream.RateLimit.PoolLabel != "" {
			rateLimit["poolLabel"] = upstream.RateLimit.PoolLabel
		}
		if upstream.RateLimit.ResetAt != "" {
			rateLimit["resetAt"] = upstream.RateLimit.ResetAt
		}
		if upstream.RateLimit.ResetTZ != "" {
			rateLimit["resetTimeZone"] = upstream.RateLimit.ResetTZ
		}
		respMap["rateLimit"] = rateLimit
	}
	if status == "active" && currentModel != "" {
		updateConnectionFreebuffModel(h.Repo, conn, currentModel)
	} else if status == "none" || status == "unauthorized" || status == "banned" {
		updateConnectionFreebuffModel(h.Repo, conn, "")
	}

	handlerutil.WriteJSON(w, http.StatusOK, freebuffSessionResponse(conn, respMap))
}

func updateConnectionFreebuffModel(repo *db.Repo, conn *models.ProviderConnection, model string) {
	if repo == nil || conn == nil {
		return
	}
	var dataMap map[string]any
	if err := json.Unmarshal([]byte(conn.Data), &dataMap); err != nil {
		dataMap = make(map[string]any)
	}
	oldModel, _ := dataMap["freebuffModel"].(string)
	if oldModel == model && model != "" {
		return
	}
	if model != "" {
		dataMap["freebuffModel"] = model
		dataMap["assignedModel"] = model
	} else {
		delete(dataMap, "freebuffModel")
		delete(dataMap, "assignedModel")
	}
	if psd, ok := dataMap["providerSpecificData"].(map[string]any); ok {
		if model != "" {
			psd["freebuffModel"] = model
			psd["assignedModel"] = model
		} else {
			delete(psd, "freebuffModel")
			delete(psd, "assignedModel")
		}
		dataMap["providerSpecificData"] = psd
	}
	newData, err := json.Marshal(dataMap)
	if err != nil {
		return
	}
	_ = repo.UpdateConnectionData(conn.ID, string(newData))
}
