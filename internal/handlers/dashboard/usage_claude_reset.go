package dashboard

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// claudeProfileURL fetches the OAuth profile including organization UUID.
var claudeProfileURL = "https://api.anthropic.com/api/oauth/profile"

// claudeResetClaimBase is the base URL for the reset_rate_limits endpoint.
var claudeResetClaimBase = "https://api.anthropic.com"

// claudeResetTarget resolves connection details for Claude reset credits.
type claudeResetTarget struct {
	ConnectionID string
	AccessToken  string
	OrgUUID      string
}

func (h *DashboardHandler) resolveClaudeResetTarget(w http.ResponseWriter, r *http.Request) (claudeResetTarget, bool) {
	connID := getURLParam(r, "connectionId")
	if connID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing connectionId")
		return claudeResetTarget{}, false
	}

	conn, err := h.Repo.GetProviderConnectionByID(connID)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return claudeResetTarget{}, false
	}
	if conn == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "connection not found")
		return claudeResetTarget{}, false
	}
	if conn.Provider != "claude" {
		writePlainError(w, http.StatusBadRequest, "Reset credits can only be redeemed for Anthropic Claude connections.")
		return claudeResetTarget{}, false
	}

	var data map[string]any
	if conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &data)
	}
	accessToken, apiKey, psd := usageCreds(data)
	if accessToken == "" {
		accessToken = apiKey
	}
	if accessToken == "" {
		writePlainError(w, http.StatusUnauthorized, "Claude OAuth access token is missing.")
		return claudeResetTarget{}, false
	}

	orgUUID := psdStr(psd, "organizationUuid", "org_uuid")
	if orgUUID == "" {
		if org, err := fetchClaudeOrgUUID(r.Context(), accessToken); err == nil && org != "" {
			orgUUID = org
			if psd == nil {
				psd = map[string]any{}
			}
			psd["organizationUuid"] = orgUUID
			if data == nil {
				data = map[string]any{}
			}
			data["providerSpecificData"] = psd
			if updatedData, err := json.Marshal(data); err == nil && h.Repo != nil {
				_ = h.Repo.UpdateConnectionData(conn.ID, string(updatedData))
			}
		}
	}

	return claudeResetTarget{
		ConnectionID: conn.ID,
		AccessToken:  accessToken,
		OrgUUID:      orgUUID,
	}, true
}

func fetchClaudeOrgUUID(ctx context.Context, accessToken string) (string, error) {
	status, _, body, err := usageDo(ctx, http.MethodGet, claudeProfileURL, map[string]string{
		"Authorization":     "Bearer " + accessToken,
		"Accept":            "application/json",
		"anthropic-beta":    claudeUsageBeta,
		"anthropic-version": claudeUsageVersion,
		"User-Agent":        claudeUsageUserAgent,
	}, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("profile status %d", status)
	}
	var resp struct {
		Organization struct {
			UUID string `json:"uuid"`
		} `json:"organization"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	return resp.Organization.UUID, nil
}

// HandleListClaudeResetCredits handles GET /api/usage/{connectionId}/claude-reset-credits.
func (h *DashboardHandler) HandleListClaudeResetCredits(w http.ResponseWriter, r *http.Request) {
	target, ok := h.resolveClaudeResetTarget(w, r)
	if !ok {
		return
	}
	if !acquireQuotaSlot(w, r) {
		return
	}

	fetchURL := claudeUsageURL
	if strings.Contains(fetchURL, "?") {
		fetchURL += "&cedar_ember=1&skip_spend=1"
	} else {
		fetchURL += "?cedar_ember=1&skip_spend=1"
	}

	status, _, body, err := usageDo(r.Context(), http.MethodGet, fetchURL, map[string]string{
		"Authorization":     "Bearer " + target.AccessToken,
		"Accept":            "application/json",
		"anthropic-beta":    claudeUsageBeta,
		"anthropic-version": claudeUsageVersion,
		"User-Agent":        claudeUsageUserAgent,
	}, nil)
	if err != nil {
		writeResetCreditError(w, fmt.Errorf("failed to fetch Claude reset credits: %w", err))
		return
	}
	if status != http.StatusOK {
		writeResetCreditError(w, fmt.Errorf("claude usage returned status %d", status))
		return
	}

	var usageData map[string]any
	_ = json.Unmarshal(body, &usageData)
	ce, _ := usageData["cedar_ember"].(map[string]any)
	if ce == nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"credits":        []any{},
			"availableCount": 0,
		})
		return
	}

	grants, _ := ce["grants"].([]any)
	credits := make([]map[string]any, 0, len(grants))
	availableTotal := 0
	for _, g := range grants {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		id := usageStr(gm["id"])
		label := usageStr(gm["label"])
		gType := usageStr(gm["type"])
		startsAt := usageStr(gm["starts_at"])
		endsAt := usageStr(gm["ends_at"])
		resetsLeft := 0
		if rl, ok := gm["resets_left"].(float64); ok {
			resetsLeft = int(rl)
		} else if rl, ok := gm["resets_left"].(int); ok {
			resetsLeft = rl
		}
		usableNow, _ := gm["usable_now"].(bool)
		if resetsLeft > 0 && usableNow {
			availableTotal += resetsLeft
		}
		clearsStr := ""
		if clears, ok := gm["clears"].([]any); ok && len(clears) > 0 {
			var parts []string
			for _, c := range clears {
				parts = append(parts, usageStr(c))
			}
			clearsStr = "Clears: " + strings.Join(parts, ", ")
		}

		credits = append(credits, map[string]any{
			"selectionToken": id,
			"resetType":      gType,
			"status":         "available",
			"grantedAt":      startsAt,
			"expiresAt":      endsAt,
			"title":          label,
			"description":    clearsStr,
		})
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"credits":        credits,
		"availableCount": availableTotal,
	})
}

// HandleConsumeClaudeResetCredit handles POST /api/usage/{connectionId}/claude-reset-credits/consume.
func (h *DashboardHandler) HandleConsumeClaudeResetCredit(w http.ResponseWriter, r *http.Request) {
	target, ok := h.resolveClaudeResetTarget(w, r)
	if !ok {
		return
	}

	var body struct {
		SelectionToken string `json:"selectionToken"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8*1024))
	if err != nil {
		writePlainError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	if body.SelectionToken == "" {
		writePlainError(w, http.StatusBadRequest, "missing selectionToken (grant_id)")
		return
	}
	if body.IdempotencyKey == "" {
		body.IdempotencyKey = newResetCreditIdempotencyKey()
	}
	if target.OrgUUID == "" {
		writePlainError(w, http.StatusBadGateway, "failed to resolve Anthropic organization UUID for connection")
		return
	}
	if !acquireQuotaSlot(w, r) {
		return
	}

	claimURL := fmt.Sprintf("%s/api/organizations/%s/reset_rate_limits", claudeResetClaimBase, target.OrgUUID)
	claimPayload := map[string]any{
		"program":    "cedar_ember",
		"grant_id":   body.SelectionToken,
		"request_id": body.IdempotencyKey,
	}
	claimBytes, _ := json.Marshal(claimPayload)

	status, _, respBody, err := usageDo(r.Context(), http.MethodPost, claimURL, map[string]string{
		"Authorization":     "Bearer " + target.AccessToken,
		"Content-Type":      "application/json",
		"Accept":            "application/json",
		"anthropic-beta":    claudeUsageBeta,
		"anthropic-version": claudeUsageVersion,
		"User-Agent":        claudeUsageUserAgent,
	}, claimBytes)

	if err != nil {
		writeResetCreditError(w, fmt.Errorf("redeem Claude reset credit request failed: %w", err))
		return
	}
	if status < 200 || status >= 300 {
		var errResp struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &errResp)
		msg := errResp.Error.Message
		if msg == "" {
			msg = errResp.Message
		}
		if msg == "" {
			msg = string(respBody)
		}
		writeResetCreditError(w, fmt.Errorf("anthropic rejected reset credit redemption (%d): %s", status, msg))
		return
	}

	if h.Repo != nil {
		if err := h.Repo.ResetConnectionHealthState(target.ConnectionID); err != nil {
			log.Warn("failed to reset connection health state", "connId", target.ConnectionID, "error", err)
		}
	}
	key := claudeUsageKey(target.AccessToken)
	claudeUsageMu.Lock()
	if e, exists := claudeUsageState[key]; exists && e != nil {
		e.expiresAt = time.Time{}
		e.cooldownTil = time.Time{}
	}
	claudeUsageMu.Unlock()

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"outcome":        "reset",
		"selectionToken": body.SelectionToken,
		"idempotencyKey": body.IdempotencyKey,
	})
}
