package oauth

import (
	"errors"
	"io"
	"net/http"
	"strings"

	json "encoding/json/v2"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/proxy/executor/mcodelease"
)

// leaseProviders are the MiniMax Code connections that can borrow the desktop
// app's sign-in. Both lanes share one protocol, so the broker is the same for
// each.
var leaseProviders = map[string]string{
	"minimax-code":        "MiniMax Desktop (Lease)",
	"minimax-code-global": "MiniMax Desktop (Lease, Global)",
}

// isLeaseProvider reports whether a provider supports lease auth mode.
func isLeaseProvider(provider string) bool {
	_, ok := leaseProviders[provider]
	return ok
}

// HandleLeaseStatus probes the MiniMax Code desktop broker: is the desktop app
// installed, running and signed in on this machine? Read-only — it leases
// nothing, so it is safe to call from the dashboard's connect button.
//
// GET /api/oauth/{provider}/lease-status?data_dir=<absolute path>
func (h *OAuthHandler) HandleLeaseStatus(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if !isLeaseProvider(provider) {
		handlerutil.WriteJSONError(w, http.StatusBadRequest,
			"lease-status only supported for minimax-code/minimax-code-global")
		return
	}
	override := strings.TrimSpace(r.URL.Query().Get("data_dir"))
	dataDir, err := mcodelease.ResolveDataDir(map[string]any{"mcodeDataDir": override})
	if err != nil {
		writeLeaseError(w, err, dataDir)
		return
	}
	status, err := mcodelease.Status(r.Context(), dataDir)
	if err != nil {
		writeLeaseError(w, err, dataDir)
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"found":       true,
		"dataDir":     dataDir,
		"status":      status.Status,
		"generation":  status.Generation,
		"expiresAtMs": status.ExpiresAtMs,
	})
}

// writeLeaseError reports a lease failure with the broker's own code, so the
// dashboard can tell "sign in to the desktop app" from "start the desktop
// app" instead of showing one generic message.
func writeLeaseError(w http.ResponseWriter, err error, dataDir string) {
	code := "INTERNAL_ERROR"
	var le *mcodelease.AuthLeaseError
	if asLeaseError(err, &le) {
		code = le.Code
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"found":   false,
		"dataDir": dataDir,
		"error":   code,
		"message": err.Error(),
	})
}

// HandleLeaseConnect creates a connection that borrows the desktop app's
// sign-in over the local auth-lease pipe. The router stores no tokens for it —
// the desktop broker owns refresh.
//
// POST /api/oauth/{provider}/lease-connect  {"dataDir":"/absolute/path"}
func (h *OAuthHandler) HandleLeaseConnect(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	name, ok := leaseProviders[provider]
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusBadRequest,
			"lease-connect only supported for minimax-code/minimax-code-global")
		return
	}
	var body struct {
		DataDir string `json:"dataDir"`
	}
	if err := json.Unmarshal(readAll(r), &body); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	dataDir, err := mcodelease.ResolveDataDir(map[string]any{"mcodeDataDir": strings.TrimSpace(body.DataDir)})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Validate availability up front, so an operator gets the broker's exact
	// failure reason instead of a connection that looks saved and fails on
	// the first request.
	if _, err := mcodelease.Lease(r.Context(), dataDir, mcodelease.MaxMinValidityMs); err != nil {
		var le *mcodelease.AuthLeaseError
		code := "INTERNAL_ERROR"
		if asLeaseError(err, &le) {
			code = le.Code
		}
		handlerutil.WriteJSON(w, http.StatusConflict, map[string]any{
			"error":   err.Error(),
			"code":    code,
			"dataDir": dataDir,
		})
		return
	}
	// Idempotent: one lease connection per (provider, data dir), so a double
	// click on the dashboard button does not create a second connection that
	// borrows the same sign-in.
	if existing := h.findLeaseConnection(provider, dataDir); existing != "" {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"connection": map[string]any{"id": existing, "provider": provider},
			"reused":     true,
		})
		return
	}
	connID, err := h.createLeaseConnection(provider, name, dataDir)
	if err != nil {
		log.Error("oauth", "lease connection create failed", "provider", provider, "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to save lease connection")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"connection": map[string]any{"id": connID, "provider": provider},
	})
}

// findLeaseConnection returns the id of the connection that already borrows
// this desktop install, or "". Idempotency is keyed on (provider, data dir)
// because that pair identifies one sign-in: a second connection would lease
// the same token and look like two accounts.
func (h *OAuthHandler) findLeaseConnection(provider, dataDir string) string {
	conns, err := h.Repo.GetProviderConnections(provider, false)
	if err != nil {
		log.Error("oauth", "lease lookup failed", "provider", provider, "error", err)
		return ""
	}
	for _, c := range conns {
		if c.AuthType != "mcode-lease" {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(c.Data), &data); err != nil {
			continue
		}
		if stored, _ := data["mcodeDataDir"].(string); stored != "" && stored == dataDir {
			return c.ID
		}
	}
	return ""
}

// createLeaseConnection stores the connection. No credential is written: the
// data column holds only the data dir the broker is resolved from, and the
// desktop app owns the token.
func (h *OAuthHandler) createLeaseConnection(provider, name, dataDir string) (string, error) {
	connID := provider + "-lease-" + randomString(12)
	data, err := json.Marshal(map[string]any{"mcodeDataDir": dataDir})
	if err != nil {
		return "", err
	}
	if err := h.Repo.CreateProviderConnectionFull(connID, provider, "mcode-lease", name, nil, string(data)); err != nil {
		return "", err
	}
	return connID, nil
}

// readAll drains the request body. The dashboard sends a small JSON object,
// so there is no streaming to preserve.
func readAll(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil
	}
	return body
}

// asLeaseError reports whether err is a broker-reported lease failure.
func asLeaseError(err error, target **mcodelease.AuthLeaseError) bool {
	return errors.As(err, target)
}
