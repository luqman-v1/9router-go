package dashboard

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"9router/proxy/internal/analyticsrange"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/keikey"
	"9router/proxy/internal/models"
)

// Usage per client API key.
//
// This is a different axis from the Overview's "Usage by API Key" table: that
// one groups usageHistory.apiKey, the upstream credential the router spent,
// while this groups usageHistory.clientKey, the key the caller presented. An
// operator who resells the gateway needs the second one — it is the key they
// sold.

// UsageByAPIKeyItem is one client-API-key row of the usage breakdown.
type UsageByAPIKeyItem struct {
	// ID is the apiKeys row the spend belongs to. Empty when the key was
	// deleted after the spend was recorded, or when the row carries no client
	// key at all.
	ID string `json:"id"`
	// Name is the key's label in the API keys page.
	Name string `json:"name"`
	// Key is the masked key, in the same form the usage ledger stores.
	Key string `json:"key"`
	// KeyDisplay is the masked key in the API keys page's own form.
	KeyDisplay string `json:"keyDisplay"`
	// Active reports whether the key is still enabled. A key that was deleted
	// is reported as inactive too.
	Active bool `json:"active"`
	// Requests, Errors, PromptTokens, CompletionTokens, CachedTokens and Cost
	// are the window's totals for this key.
	Requests         int     `json:"requests"`
	Errors           int     `json:"errors"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	CachedTokens     int     `json:"cachedTokens"`
	Cost             float64 `json:"cost"`
	// LastUsed is when the key last made a request inside the window.
	LastUsed string `json:"lastUsed"`
}

// UsageByAPIKeyResponse is the body of GET /api/usage/api-keys.
type UsageByAPIKeyResponse struct {
	// Period names the window the numbers describe, echoed so a client can
	// tell an empty window from one the server answered for a different
	// period.
	Period string `json:"period"`
	// Keys are the per-key rows, ordered by spend.
	Keys []UsageByAPIKeyItem `json:"keys"`
	// Totals are the window's totals across every key.
	Totals UsageByAPIKeyTotals `json:"totals"`
}

// UsageByAPIKeyTotals are the window's totals across all client keys.
type UsageByAPIKeyTotals struct {
	Requests         int     `json:"requests"`
	Errors           int     `json:"errors"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	CachedTokens     int     `json:"cachedTokens"`
	Cost             float64 `json:"cost"`
}

// HandleGetUsageByAPIKey handles GET /api/usage/api-keys.
//
// The window is the `period` parameter the rest of the Usage page uses,
// resolved through analyticsrange so this panel and the Overview cannot
// disagree about what "7d" means.
func (h *DashboardHandler) HandleGetUsageByAPIKey(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("period")
	win := analyticsrange.Resolve(raw, time.Now())
	since := win.Cutoff().Format(time.RFC3339)
	rows, err := h.Repo.GetUsageGroupByClientKeySince(since)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	keys := h.resolveUsageClientKeys()
	resp := UsageByAPIKeyResponse{
		Period: win.Label(raw),
		Keys:   make([]UsageByAPIKeyItem, 0, len(rows)),
	}
	for _, row := range rows {
		item := UsageByAPIKeyItem{
			Requests:         row.Requests,
			Errors:           row.Errors,
			PromptTokens:     row.PromptTokens,
			CompletionTokens: row.CompletionTokens,
			CachedTokens:     row.CachedTokens,
			Cost:             row.Cost,
			LastUsed:         row.LastUsed,
		}
		switch row.ClientKey {
		case "":
			// A request that carried no client key — the media path writes
			// none — still spent tokens, so it gets a bucket of its own
			// rather than disappearing from the totals.
			item.Name = "No client key"
			item.Key = "***"
		default:
			if info, ok := keys.lookup(row.ClientKey); ok {
				item.ID = info.id
				item.Name = apiKeyLabel(info.name, info.display, row.ClientKey)
				item.KeyDisplay = info.display
				item.Active = info.active
				// The ledger's own mask is what the row carries, so the
				// panel shows the same string the ledger holds.
				item.Key = row.ClientKey
			} else {
				// The key was deleted after it spent. The spend is still
				// real, so the row is reported under its masked form.
				item.Name = "Deleted key"
				item.Key = row.ClientKey
			}
		}
		resp.Keys = append(resp.Keys, item)
		resp.Totals.Requests += item.Requests
		resp.Totals.Errors += item.Errors
		resp.Totals.PromptTokens += item.PromptTokens
		resp.Totals.CompletionTokens += item.CompletionTokens
		resp.Totals.CachedTokens += item.CachedTokens
		resp.Totals.Cost += item.Cost
	}
	sortUsageByAPIKey(resp.Keys)
	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

// usageClientKeyInfo is what the panel needs to name a masked ledger row.
type usageClientKeyInfo struct {
	id      string
	name    string
	display string
	active  bool
}

// usageClientKeyIndex resolves a masked ledger row to the key it belongs to.
//
// The ledger stores the masked form of the key the caller presented, so the
// lookup is by mask. The suffix index is the fallback for a row whose mask
// does not match the stored one — a key whose secret was rotated, or a row
// written before the mask format changed — and the last four characters are
// the part that tells two keys of the same machine apart.
type usageClientKeyIndex struct {
	byMask   map[string]usageClientKeyInfo
	bySuffix map[string]usageClientKeyInfo
}

// lookup finds the key a masked ledger row belongs to.
func (m usageClientKeyIndex) lookup(mask string) (usageClientKeyInfo, bool) {
	if info, ok := m.byMask[mask]; ok {
		return info, true
	}
	suffix := keyTail(mask)
	if suffix == "" {
		return usageClientKeyInfo{}, false
	}
	if info, ok := m.bySuffix[suffix]; ok {
		return info, true
	}
	return usageClientKeyInfo{}, false
}

// resolveUsageClientKeys reads the key rows and indexes them by mask.
func (h *DashboardHandler) resolveUsageClientKeys() usageClientKeyIndex {
	index := usageClientKeyIndex{
		byMask:   map[string]usageClientKeyInfo{},
		bySuffix: map[string]usageClientKeyInfo{},
	}
	if h.Repo == nil {
		return index
	}
	apiKeys, err := h.Repo.GetApiKeys()
	if err != nil {
		return index
	}
	for _, k := range apiKeys {
		if k == nil {
			continue
		}
		info := usageClientKeyInfo{
			id:      k.ID,
			name:    apiKeyName(k.Name),
			display: apiKeyDisplayOf(k),
			active:  k.IsActive == 1,
		}
		if db.IsHashedKeyRow(k) {
			// The secret is a verifier now; only the display form is left.
			display := ""
			if k.KeyDisplay != nil {
				display = *k.KeyDisplay
			}
			if suffix := keyTail(display); suffix != "" {
				index.bySuffix[suffix] = info
			}
			continue
		}
		if mask := handlerutil.MaskAPIKey(k.Key); mask != "" && mask != "***" {
			index.byMask[mask] = info
		}
		if suffix := keyTail(k.Key); suffix != "" {
			if _, exists := index.bySuffix[suffix]; !exists {
				index.bySuffix[suffix] = info
			}
		}
	}
	return index
}

// keyTail returns the last four characters of a key or a masked key, which is
// the part that tells two keys of the same machine apart.
func keyTail(key string) string {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" || trimmed == "***" {
		return ""
	}
	if handlerutil.RuneCount(trimmed) <= 4 {
		return trimmed
	}
	return handlerutil.RuneSuffix(trimmed, 4)
}

// apiKeyLabel returns the key's name, falling back to the masked key so a row
// without a name still reads as something a person can recognize.
func apiKeyLabel(name, display, ledgerMask string) string {
	if strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	if strings.TrimSpace(display) != "" {
		return strings.TrimSpace(display)
	}
	return ledgerMask
}

// apiKeyName returns the key's label, or an empty string when it has none.
func apiKeyName(name *string) string {
	if name == nil {
		return ""
	}
	return strings.TrimSpace(*name)
}

// apiKeyDisplayOf returns the masked key in the API keys page's own form.
func apiKeyDisplayOf(k *models.APIKey) string {
	if k.KeyDisplay != nil && *k.KeyDisplay != "" {
		return *k.KeyDisplay
	}
	if db.IsHashedKeyRow(k) {
		return ""
	}
	return keikey.Mask(k.Key)
}

// sortUsageByAPIKey orders the rows by spend, heaviest first, with the key
// name as the tiebreak so the order is stable across refreshes.
func sortUsageByAPIKey(items []UsageByAPIKeyItem) {
	sort.SliceStable(items, func(a, b int) bool {
		x, y := items[a], items[b]
		if x.PromptTokens+x.CompletionTokens != y.PromptTokens+y.CompletionTokens {
			return x.PromptTokens+x.CompletionTokens > y.PromptTokens+y.CompletionTokens
		}
		if x.Requests != y.Requests {
			return x.Requests > y.Requests
		}
		return x.Name < y.Name
	})
}
