package dashboard

import (
	"io"
	"net/http"
	"net/netip"
	"strings"

	jsonv2 "encoding/json/v2"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// HandleListBlockedIPs handles GET /api/blocked-ips.
func (h *DashboardHandler) HandleListBlockedIPs(w http.ResponseWriter, r *http.Request) {
	entries, err := h.Repo.ListBlockedIPs()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entries == nil {
		entries = []db.BlockedIPEntry{}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"blocked": entries,
	})
}

// HandleBlockIP handles POST /api/blocked-ips.
func (h *DashboardHandler) HandleBlockIP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer func() { _ = r.Body.Close() }()
	var data map[string]any
	if err := jsonv2.Unmarshal(body, &data); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ip, _ := data["ip"].(string)
	if strings.TrimSpace(ip) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing ip")
		return
	}
	if _, err := netip.ParseAddr(strings.TrimSpace(ip)); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid ip")
		return
	}
	country, _ := data["country"].(string)
	reason, _ := data["reason"].(string)
	entry, err := h.Repo.BlockIP(ip, country, reason, "dashboard")
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusCreated, entry)
}

// HandleUnblockIP handles DELETE /api/blocked-ips?ip=...
func (h *DashboardHandler) HandleUnblockIP(w http.ResponseWriter, r *http.Request) {
	ip := r.URL.Query().Get("ip")
	if strings.TrimSpace(ip) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing ip")
		return
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid ip")
		return
	}
	norm := addr.Unmap().String()
	if err := h.Repo.UnblockIP(ip); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"unblocked": norm,
	})
}
