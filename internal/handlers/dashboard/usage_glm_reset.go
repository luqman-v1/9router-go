package dashboard

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
	"github.com/google/uuid"
)

// HandleGetGLMResetCards handles GET /api/usage/{connectionId}/reset-cards.
// Resolves the connection, verifies the provider is glm (international),
// fetches reset cards, and returns them as JSON.
func (h *DashboardHandler) HandleGetGLMResetCards(w http.ResponseWriter, r *http.Request) {
	connID := getURLParam(r, "connectionId")
	if connID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing connectionId")
		return
	}

	conn, err := h.Repo.GetProviderConnectionByID(connID)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conn == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "connection not found")
		return
	}
	if conn.Provider != "glm" {
		writePlainError(w, http.StatusBadRequest,
			"Reset cards are only available for GLM (international) connections.")
		return
	}

	var data map[string]any
	if conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &data)
	}
	accessToken, apiKey, _ := usageCreds(data)
	token := firstNonEmptyStr(apiKey, accessToken)
	if token == "" {
		writePlainError(w, http.StatusBadRequest, "GLM API key or access token is missing.")
		return
	}

	cards, err := fetchGLMResetCards(r.Context(), token)
	if err != nil {
		status := http.StatusBadGateway
		msg := err.Error()
		if strings.Contains(msg, "unauthorized") || strings.Contains(msg, "401") {
			status = http.StatusUnauthorized
			msg = "GLM token expired. Re-authenticate the connection to refresh reset cards."
		}
		writePlainError(w, status, msg)
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, cards)
}

// HandleRedeemGLMResetCard handles POST /api/usage/{connectionId}/reset-cards/redeem.
// Takes { recordId, type } and calls the GLM reset-card use endpoint.
func (h *DashboardHandler) HandleRedeemGLMResetCard(w http.ResponseWriter, r *http.Request) {
	connID := getURLParam(r, "connectionId")
	if connID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing connectionId")
		return
	}

	conn, err := h.Repo.GetProviderConnectionByID(connID)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conn == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "connection not found")
		return
	}
	if conn.Provider != "glm" {
		writePlainError(w, http.StatusBadRequest,
			"Reset cards can only be redeemed for GLM (international) connections.")
		return
	}

	var data map[string]any
	if conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &data)
	}
	accessToken, apiKey, _ := usageCreds(data)
	token := firstNonEmptyStr(apiKey, accessToken)
	if token == "" {
		writePlainError(w, http.StatusBadRequest, "GLM API key or access token is missing.")
		return
	}

	var body struct {
		RecordID string `json:"recordId"`
		Type     string `json:"type"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil {
		writePlainError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	if len(raw) > 0 {
		if uerr := json.Unmarshal(raw, &body); uerr != nil {
			writePlainError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	body.RecordID = strings.TrimSpace(body.RecordID)
	body.Type = strings.TrimSpace(body.Type)
	if body.RecordID == "" {
		writePlainError(w, http.StatusBadRequest, "recordId is required")
		return
	}
	if body.Type == "" {
		writePlainError(w, http.StatusBadRequest, "type is required")
		return
	}
	if body.Type != "FIVE_HOUR" && body.Type != "WEEK" {
		writePlainError(w, http.StatusBadRequest, "type must be FIVE_HOUR or WEEK")
		return
	}

	reqID := uuid.New().String()
	url := "https://api.z.ai/api/biz/customer-package-reset/use"
	payload := map[string]any{
		"targetType": "PERSONAL",
		"resetType":  body.Type,
		"recordId":   body.RecordID,
		"requestId":  reqID,
	}
	headers := map[string]string{
		"Authorization": "Bearer " + strings.TrimSpace(token),
		"Accept":        "application/json",
		"Content-Type":  "application/json",
	}

	payloadBytes, _ := json.Marshal(payload)
	status, _, out, err := usageDo(r.Context(), http.MethodPost, url, headers, payloadBytes)
	if err != nil {
		writePlainError(w, http.StatusBadGateway, fmt.Sprintf("GLM reset card redeem error: %v", err))
		return
	}
	if status == http.StatusUnauthorized {
		writePlainError(w, http.StatusUnauthorized, "GLM token expired. Re-authenticate the connection.")
		return
	}
	if status != http.StatusOK {
		msg := fmt.Sprintf("GLM reset card API error (%d)", status)
		if len(out) > 0 {
			if t := strings.TrimSpace(string(out)); t != "" {
				if len(t) > 200 {
					t = t[:200]
				}
				msg += ": " + t
			}
		}
		writePlainError(w, http.StatusBadGateway, msg)
		return
	}

	// Try to parse the response for a success indicator
	respData := usageJSON(out)
	success := true
	code := "SUCCESS"
	message := "GLM usage reset successfully."
	if respData != nil {
		if c, ok := respData["code"].(string); ok && c != "" {
			code = c
		}
		if m, ok := respData["message"].(string); ok && m != "" {
			message = m
		}
		// Check for failure codes
		if code != "SUCCESS" {
			success = false
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"reset":     success,
		"code":      code,
		"message":   message,
		"recordId":  body.RecordID,
		"type":      body.Type,
		"requestId": reqID,
	})
}
