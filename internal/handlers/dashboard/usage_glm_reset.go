package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/handlerutil"
	"github.com/google/uuid"
)

var (
	glmResetMu       sync.Mutex
	glmResetInFlight = make(map[string]bool)
)

// HandleGetGLMResetCards handles GET /api/usage/{connectionId}/glm-reset-cards.
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

// HandleRedeemGLMResetCard handles POST /api/usage/{connectionId}/glm-reset-cards.
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

	// In-flight lock per connection.
	glmResetMu.Lock()
	if glmResetInFlight[connID] {
		glmResetMu.Unlock()
		handlerutil.WriteJSONError(w, http.StatusConflict, "A reset-card redemption is already in progress for this connection.")
		return
	}
	glmResetInFlight[connID] = true
	glmResetMu.Unlock()

	defer func() {
		glmResetMu.Lock()
		delete(glmResetInFlight, connID)
		glmResetMu.Unlock()
	}()

	// Re-read cards inventory to verify the card exists and is not expired.
	cardsResp, err := fetchGLMResetCards(r.Context(), token)
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

	// Find the requested card and check expiry.
	var selectedCard *GLMResetCard
	cardsList, _ := cardsResp["cards"].([]any)
	for _, rawCard := range cardsList {
		cardMap, _ := rawCard.(map[string]any)
		if cardMap == nil {
			continue
		}
		if cardMap["recordId"] == body.RecordID && cardMap["type"] == body.Type {
			expiresAtStr, _ := cardMap["expiresAt"].(string)
			if expiresAtStr != "" {
				if expTime, err := time.Parse(time.RFC3339, expiresAtStr); err == nil {
					if time.Now().After(expTime) {
						handlerutil.WriteJSONError(w, http.StatusConflict, "Selected reset card has expired.")
						return
					}
				}
			}
			selectedCard = &GLMResetCard{
				RecordID:  body.RecordID,
				Type:      body.Type,
				ExpiresAt: expiresAtStr,
			}
			break
		}
	}
	if selectedCard == nil {
		handlerutil.WriteJSONError(w, http.StatusConflict, "Selected reset card is no longer available.")
		return
	}

	reqID := uuid.New().String()
	url := glmResetUseURL
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
		handlerutil.WriteJSON(w, http.StatusBadGateway, map[string]any{
			"reset":       false,
			"alreadyUsed": false,
			"uncertain":   true,
			"code":        "uncertain",
			"message":     "Reset outcome is unknown. Check the Z.ai dashboard before trying again.",
		})
		return
	}

	// Parse Z.ai response: { success: bool, code: any, message: string, alreadyUsed: bool }
	var respData struct {
		Success     bool   `json:"success"`
		AlreadyUsed bool   `json:"alreadyUsed"`
		Code        any    `json:"code"`
		Message     string `json:"message"`
	}
	_ = json.Unmarshal(out, &respData)

	// Check code is 0 or 200 (numeric or string)
	codeOk := false
	switch v := respData.Code.(type) {
	case float64:
		codeOk = (v == 0 || v == 200)
	case int:
		codeOk = (v == 0 || v == 200)
	case string:
		codeOk = (v == "0" || v == "200")
	}

	// Determine success: HTTP 200 AND success == true AND alreadyUsed != true AND (code == 0 || code == 200)
	success := false
	code := "ERROR"
	if status == http.StatusOK && respData.Success && !respData.AlreadyUsed && codeOk {
		success = true
		code = "SUCCESS"
	}

	if !success {
		// All non-success wire responses map to 502 / uncertain: true, matching upstream open-sse/services/usage/glm.js:139.
		// Never forward upstream 401 as HTTP status because client interceptors would treat it as dashboard session expiration.
		handlerutil.WriteJSON(w, http.StatusBadGateway, map[string]any{
			"reset":       false,
			"alreadyUsed": respData.AlreadyUsed,
			"uncertain":   true,
			"code":        "uncertain",
			"message":     "Reset outcome is unknown. Check the Z.ai dashboard before trying again.",
		})
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"reset":       true,
		"alreadyUsed": false,
		"uncertain":   false,
		"code":        code,
		"message":     "GLM usage reset successfully.",
		"recordId":    body.RecordID,
		"type":        body.Type,
		"requestId":   reqID,
	})
}
