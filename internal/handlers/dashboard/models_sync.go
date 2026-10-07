package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// HandleSyncProviderModels handles POST /api/models/sync.
//
// It re-reads each connection's upstream catalogue and reconciles the result
// against the recorded deprecations: a model the provider still publishes is
// alive and loses its badge.
//
// A model the catalogue *omits* is deliberately not badged. Catalogues are
// frequently partial — a scoped key, a paginated feed, a node behind a feature
// flag — so absence is weak evidence, and acting on it would blacklist healthy
// models. A model is retired only when a live request gets a 410 naming it, or
// when one comes back by serving a request (#179).
//
// The provider query parameter is optional: "" syncs every provider that can
// list models.
func (h *DashboardHandler) HandleSyncProviderModels(w http.ResponseWriter, r *http.Request) {
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	conns, err := h.Repo.GetProviderConnections(provider, true)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(conns) == 0 {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "no active connections to sync")
		return
	}

	synced, failed, skipped, revived := 0, 0, 0, 0
	var lastErr error
	for _, conn := range conns {
		if conn == nil {
			continue
		}
		live, fetchErr, listable := h.fetchLiveModelIDs(r, conn)
		if !listable {
			// The provider has no catalogue endpoint at all. That is a
			// property of the provider, not a failed sync, and reporting it as
			// one would turn every Sync Models click on such a provider into a
			// 502 the operator cannot act on.
			skipped++
			continue
		}
		if fetchErr != nil {
			failed++
			lastErr = fetchErr
			log.Warn("models-sync", "catalogue unreachable", "provider", conn.Provider, "conn", conn.ID, "error", fetchErr)
			continue
		}
		if len(live) == 0 {
			continue
		}
		cleared, clearErr := h.Repo.RetainLiveModels(conn.Provider, live)
		if clearErr != nil {
			failed++
			lastErr = clearErr
			continue
		}
		synced++
		revived += cleared
		log.Info("models-sync", "catalogue refreshed", "provider", conn.Provider, "models", len(live), "revived", cleared)
	}

	if synced == 0 && skipped == 0 && failed > 0 {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "model sync failed: "+lastErr.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":      true,
		"provider":     provider,
		"synced":       synced,
		"failed":       failed,
		"skipped":      skipped,
		"revived":      revived,
		"lastError":    errText(lastErr),
		"catalogState": providers.GetCatalogState(),
	})
}

// errText renders an optional error for a JSON field.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// fetchLiveModelIDs returns the set of model ids a connection's upstream
// currently publishes, keyed the way deprecations are keyed.
//
// listable is false when the provider has no catalogue endpoint, which is a
// property of the provider rather than a sync failure — the dashboard answers
// 400 for those, and treating that as an error would make every provider
// without a catalogue report a broken sync.
func (h *DashboardHandler) fetchLiveModelIDs(r *http.Request, conn *models.ProviderConnection) (map[string]bool, error, bool) {
	capture := newModelsCapture()
	h.writeConnectionModels(capture, r, conn.ID)
	if capture.status == http.StatusBadRequest {
		return nil, nil, false
	}
	if capture.status != http.StatusOK {
		return nil, fmt.Errorf("catalogue unavailable: %s", strings.TrimSpace(capture.body.String())), true
	}

	var resp struct {
		Models []struct {
			ID    string `json:"id"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.Unmarshal(capture.body.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("invalid catalogue response: %w", err), true
	}

	live := make(map[string]bool, len(resp.Models))
	for _, m := range resp.Models {
		id := m.ID
		if id == "" {
			id = m.Model
		}
		if id == "" {
			continue
		}
		live[providers.DeprecationKey(conn.Provider, id)] = true
	}
	return live, nil, true
}

// modelsCapture is a ResponseWriter that keeps what the discovery handler wrote
// instead of sending it anywhere, so sync can reuse that handler and read the
// catalogue off it.
type modelsCapture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newModelsCapture() *modelsCapture {
	return &modelsCapture{header: make(http.Header), status: http.StatusOK}
}

func (c *modelsCapture) Header() http.Header { return c.header }

func (c *modelsCapture) Write(p []byte) (int, error) { return c.body.Write(p) }

func (c *modelsCapture) WriteHeader(status int) { c.status = status }
