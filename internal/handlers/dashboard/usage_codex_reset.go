package dashboard

import (
	"crypto/rand"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"time"

	"9router/proxy/internal/codexquota"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
)

// Codex reset credits — port of OmniRoute's
// src/lib/usage/codexResetCredits.ts. The dashboard already drew the credit
// counter (fetchCodexUsage reports resetCredits.availableCount), but the
// button had no handler, so the counter was read-only.
//
// The refresher is passed as nil, matching fetchCodexUsage: the dashboard has
// no OAuth refresher of its own, and importing the chat package's would pull
// provider-specific token persistence across package boundaries. The
// 401/403 retry that codexquota.ConsumeResetCredit supports is therefore
// dormant here rather than half-implemented, and an expired token surfaces as
// a typed 401 the user can act on by re-authorizing.

// codexResetCreditTarget resolves the connection behind a reset-credit request.
type codexResetCreditTarget struct {
	ConnectionID string
	AccessToken  string
	AccountID    string
}

// resolveCodexResetCreditTarget validates that the connection is one Codex can
// redeem credits for, and returns the token plus the account id the wham
// endpoints scope the credits to.
func (h *DashboardHandler) resolveCodexResetCreditTarget(w http.ResponseWriter, r *http.Request) (codexResetCreditTarget, bool) {
	connID := getURLParam(r, "connectionId")
	if connID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing connectionId")
		return codexResetCreditTarget{}, false
	}

	conn, err := h.Repo.GetProviderConnectionByID(connID)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return codexResetCreditTarget{}, false
	}
	if conn == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "connection not found")
		return codexResetCreditTarget{}, false
	}
	if conn.Provider != "codex" {
		writePlainError(w, http.StatusBadRequest,
			"Reset credits can only be redeemed for OpenAI Codex accounts.")
		return codexResetCreditTarget{}, false
	}
	if conn.AuthType != "oauth" {
		writePlainError(w, http.StatusBadRequest,
			"Codex reset credits require an OAuth connection.")
		return codexResetCreditTarget{}, false
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
		writePlainError(w, http.StatusUnauthorized, "Codex OAuth access token is missing.")
		return codexResetCreditTarget{}, false
	}

	return codexResetCreditTarget{
		ConnectionID: conn.ID,
		AccessToken:  accessToken,
		AccountID:    psdStr(psd, "chatgptAccountId", "chatgpt_account_id"),
	}, true
}

// writeResetCreditError maps a typed reset-credit failure onto the dashboard's
// flat error shape, keeping the code so the UI can tell "no credit" from
// "nothing to reset" instead of showing one generic message.
//
// A 401 is never passed through. In this app 401 means "your dashboard
// session is invalid" — the API client treats any 401 as an expired session
// and logs the user out (web/src/api/client.ts:476). Codex rejecting its own
// token is an upstream failure, not a dashboard logout, so it is reported as
// 502 with the upstream code intact. Forwarding it verbatim bounced the whole
// dashboard to /login the moment the chooser was opened.
func writeResetCreditError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	var typed *codexquota.ResetCreditError
	if errors.As(err, &typed) {
		status = typed.Status
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			status = http.StatusBadGateway
		}
	}
	payload := map[string]any{"outcome": "error"}
	if typed != nil {
		payload["error"] = typed.Message
		payload["code"] = typed.Code
	} else {
		payload["error"] = err.Error()
	}
	body, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// HandleListCodexResetCredits handles GET /api/usage/{connectionId}/reset-credits.
func (h *DashboardHandler) HandleListCodexResetCredits(w http.ResponseWriter, r *http.Request) {
	target, ok := h.resolveCodexResetCreditTarget(w, r)
	if !ok {
		return
	}
	if !acquireQuotaSlot(w, r) {
		return
	}

	list, err := codexquota.ListResetCredits(r.Context(), nil, target.AccessToken, target.AccountID, nil, time.Now())
	if err != nil {
		writeResetCreditError(w, err)
		return
	}

	credits := make([]map[string]any, 0, len(list.Credits))
	for _, credit := range list.Credits {
		// selectionToken is the field name the dashboard selects with; the
		// dashboard is already authenticated and owns the account, so the
		// indirection OmniRoute adds is a rename here, not a second secret.
		credits = append(credits, map[string]any{
			"selectionToken": credit.ID,
			"resetType":      credit.ResetType,
			"status":         credit.Status,
			"grantedAt":      credit.GrantedAt,
			"expiresAt":      credit.ExpiresAt,
			"title":          credit.Title,
			"description":    credit.Desc,
		})
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"credits":        credits,
		"availableCount": list.AvailableCount,
	})
}

// HandleConsumeCodexResetCredit handles POST
// /api/usage/{connectionId}/reset-credits/consume.
func (h *DashboardHandler) HandleConsumeCodexResetCredit(w http.ResponseWriter, r *http.Request) {
	target, ok := h.resolveCodexResetCreditTarget(w, r)
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

	// A key the client supplies is reused so a retried click cannot redeem
	// twice; otherwise one is minted per request.
	if body.IdempotencyKey == "" {
		body.IdempotencyKey = newResetCreditIdempotencyKey()
	}
	if !acquireQuotaSlot(w, r) {
		return
	}

	outcome, credit, err := codexquota.ConsumeResetCredit(
		r.Context(), nil, target.AccessToken, target.AccountID,
		body.IdempotencyKey, body.SelectionToken, nil, time.Now(),
	)
	if err != nil {
		writeResetCreditError(w, err)
		return
	}
	chat.ClearCodexQuotaBlock(target.ConnectionID)
	if h.Repo != nil {
		_ = h.Repo.ResetProviderHealth("codex", "")
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"outcome":        outcome,
		"selectionToken": credit.ID,
		"idempotencyKey": body.IdempotencyKey,
	})
}

// newResetCreditIdempotencyKey mints a key for a single redeem attempt.
func newResetCreditIdempotencyKey() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// A time-based fallback still deduplicates a retried click within the
		// same second, which is the failure it exists to prevent.
		return "reset-" + time.Now().UTC().Format("20060102T150405.000000000")
	}
	return "reset-" + hex.EncodeToString(buf)
}
