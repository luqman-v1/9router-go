package oauth

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/proxy/executor"
)

const freebuffSessionRequestTimeout = 15 * time.Second

// freebuffUpstreamSession is the part of the upstream session payload the
// dashboard needs in order to describe, and later release, the held seat.
type freebuffUpstreamSession struct {
	CurrentModel string
	InstanceID   string
	ExpiresAt    string
}

// fetchFreebuffUpstreamSession reads the caller's current Freebuff session.
// A nil session with a nil error means the account holds no seat, which is a
// normal precondition for a switch rather than a failure.
func fetchFreebuffUpstreamSession(ctx context.Context, token string) (*freebuffUpstreamSession, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, freebuffAPIBaseURL+"/api/v1/freebuff/session", nil)
	if err != nil {
		return nil, fmt.Errorf("create session request failed: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", constants.FreebuffClientUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := executor.DoFreebuffHTTP(ctx, nil, req)
	if err != nil {
		return nil, fmt.Errorf("freebuff session request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusForbidden ||
		resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read freebuff session response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("freebuff session returned %d", resp.StatusCode)
	}

	var upstream struct {
		Status       string `json:"status"`
		CurrentModel string `json:"currentModel"`
		Model        string `json:"model"`
		InstanceID   string `json:"instanceId"`
		ExpiresAt    string `json:"expiresAt"`
	}
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, fmt.Errorf("parse freebuff session response: %w", err)
	}

	currentModel := upstream.CurrentModel
	if currentModel == "" {
		currentModel = upstream.Model
	}
	status := strings.ToLower(strings.TrimSpace(upstream.Status))
	if status == "" && (currentModel != "" || upstream.InstanceID != "") {
		status = "active"
	}
	if status != "active" {
		return nil, nil
	}

	return &freebuffUpstreamSession{
		CurrentModel: currentModel,
		InstanceID:   upstream.InstanceID,
		ExpiresAt:    upstream.ExpiresAt,
	}, nil
}

// HandleFreebuffSessionSwitch changes the model of a Freebuff session without
// waiting for it to expire.
//
// Freebuff serves one model per session and a session lives an hour even when
// idle, so a request for another model is rejected with `model_locked` until
// the seat is released. This mirrors the CLI's explicit model-pick path
// (cli/src/hooks/use-freebuff-session.ts): end the held session, then admit a
// fresh one on the requested model. Background requests deliberately do NOT
// take this path upstream, so this endpoint must stay tied to a deliberate
// user action — each switch spends a new session from the daily allowance.
// POST /api/oauth/freebuff/session/switch
func (h *OAuthHandler) HandleFreebuffSessionSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		handlerutil.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		ConnectionID      string `json:"connectionId"`
		ConnectionIDSnake string `json:"connection_id"`
		Model             string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing model")
		return
	}

	connectionID := req.ConnectionID
	if connectionID == "" {
		connectionID = req.ConnectionIDSnake
	}

	conn, err := h.freebuffConnection(connectionID)
	if err != nil {
		log.Error("oauth", "failed to query freebuff connection", "conn", connectionID, "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to query connection")
		return
	}
	if conn == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "freebuff connection not found")
		return
	}

	token := freebuffConnectionToken(conn)
	if token == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "freebuff connection has no auth token")
		return
	}

	ctx := r.Context()
	current, err := fetchFreebuffUpstreamSession(ctx, token)
	if err != nil {
		log.Error("oauth", "freebuff session lookup failed before switch", "conn", conn.ID, "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}

	// Already on the requested model: release nothing and report the session as
	// it stands, so a click on the current row cannot burn a fresh seat.
	if current != nil && current.CurrentModel == model {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"status":       "active",
			"currentModel": current.CurrentModel,
			"instanceId":   current.InstanceID,
			"expiresAt":    current.ExpiresAt,
			"switched":     false,
		})
		return
	}

	instanceID := ""
	if current != nil {
		instanceID = current.InstanceID
	}

	var store executor.LeaseStore
	if h.Repo != nil {
		store = h.Repo
	}
	result, err := executor.SwitchFreebuffModelWithStore(ctx, store, nil, freebuffAPIBaseURL, token, instanceID, model, nil)
	if err != nil {
		// The held seat survives a failed switch, so nothing is lost: the user
		log.Error("oauth", "freebuff model switch failed", "conn", conn.ID, "model", model, "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "freebuff model switch failed: "+err.Error())
		return
	}

	log.Info("oauth", "freebuff model switched", "conn", conn.ID, "model", model, "refund", result.FreebucksRefund)
	updateConnectionFreebuffModel(h.Repo, conn, model)
	resp := map[string]any{
		"status":       "active",
		"currentModel": result.Model,
		"instanceId":   result.InstanceID,
		"switched":     true,
	}
	if !result.ExpiresAt.IsZero() {
		resp["expiresAt"] = result.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if result.FreebucksRefund > 0 {
		resp["freebucksRefund"] = result.FreebucksRefund
	}
	handlerutil.WriteJSON(w, http.StatusOK, resp)
}
