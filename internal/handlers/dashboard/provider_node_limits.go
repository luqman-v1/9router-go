package dashboard

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/middleware"
)

// Provider-node capacity limits (docs/provider-limits-plan.md, PR4).
//
// A custom provider node is one upstream endpoint, so its limits are capacity
// rather than one caller's allowance: rpmLimit/tpmLimit bound what the endpoint
// tolerates, concurrencyLimit how many requests may be in flight, queueTimeoutMs
// how long one may wait for a slot, tokenQuota a lifetime token ceiling and
// contractExpiresAt the day the subscription behind it stops working. The
// enforcement lives in internal/handlers/chat (PR2/PR3); this file is the
// operator's view of it.
//
// The PUT merges instead of replacing, because a field the dashboard did not send
// is not a field the operator cleared: an omitted number keeps its stored value,
// an explicit 0 clears it, and an explicit number sets it. That is the same
// tri-state apiKeys already uses (mergeLimit), so an operator who learned one
// editor does not have to learn a second.

// ProviderNodeLimitsResponse is a node's policy plus the usage it is measured
// against, so the dashboard can render both from one read.
type ProviderNodeLimitsResponse struct {
	RPM            int `json:"rpm"`
	TPM            int `json:"tpm"`
	Concurrency    int `json:"concurrency"`
	TokenQuota     int `json:"tokenQuota"`
	QueueTimeoutMs int `json:"queueTimeoutMs"`
	// QuotaResetID is the usageHistory row the token quota is measured from, and
	// the reason usedTokens can fall back to zero without the history changing.
	QuotaResetID      int    `json:"quotaResetId"`
	ContractExpiresAt string `json:"contractExpiresAt,omitempty"`

	// UsedTokens is the number the quota gate compares against TokenQuota: the
	// lifetime prompt+completion sum since QuotaResetID, carryover included.
	UsedTokens int `json:"usedTokens"`
	// Expired mirrors the dispatch gate's own verdict, so a dashboard that shows
	// "expired" and a gateway that refuses cannot disagree about the same row.
	Expired bool `json:"expired"`

	// Live window and in-flight figures come from the in-process limiter. They are
	// zero when no limiter is wired, which is what a handler constructed outside a
	// boot sees — the same shape an unlimited node reports, so a caller that must
	// tell "unused" from "unobserved" reads the policy next to it.
	RPMUsedWindow     int `json:"rpmUsedWindow"`
	ActiveConcurrency int `json:"activeConcurrency"`
}

// providerNodeLimitsFrom assembles the response from values the caller already
// holds, so a list read costs one query for the policies and one for the usage
// rather than a read per node.
func (h *DashboardHandler) providerNodeLimitsFrom(nodeID string, limits db.ProviderLimits, used int) ProviderNodeLimitsResponse {
	res := ProviderNodeLimitsResponse{
		RPM:               limits.RPM,
		TPM:               limits.TPM,
		Concurrency:       limits.Concurrency,
		TokenQuota:        limits.TokenQuota,
		QueueTimeoutMs:    limits.QueueTimeoutMs,
		QuotaResetID:      limits.QuotaResetID,
		ContractExpiresAt: limits.ContractExpiresAt,
		UsedTokens:        used,
		Expired:           limits.Expired(time.Now()),
	}
	if h.Limiter != nil {
		res.RPMUsedWindow, res.ActiveConcurrency = h.Limiter.Snapshot(middleware.ProviderScopeKey(nodeID))
	}
	return res
}

// providerNodeLimits reads one node's usage before assembling the response.
func (h *DashboardHandler) providerNodeLimits(ctx context.Context, nodeID string, limits db.ProviderLimits) (ProviderNodeLimitsResponse, error) {
	used, err := h.Repo.ProviderTokenUsage(ctx, nodeID)
	if err != nil {
		return ProviderNodeLimitsResponse{}, err
	}
	return h.providerNodeLimitsFrom(nodeID, limits, used), nil
}

// HandleUpdateProviderNodeLimits handles PUT /api/provider-nodes/{id}/limits.
//
// The node must exist: a PUT to an id that names no row is a 404 rather than a
// silent success, because the dashboard would otherwise report a policy saved
// against a node the operator cannot find afterwards.
func (h *DashboardHandler) HandleUpdateProviderNodeLimits(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing provider node id")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	var patch struct {
		RPM               *int    `json:"rpm"`
		TPM               *int    `json:"tpm"`
		Concurrency       *int    `json:"concurrency"`
		TokenQuota        *int    `json:"tokenQuota"`
		QueueTimeoutMs    *int    `json:"queueTimeoutMs"`
		ContractExpiresAt *string `json:"contractExpiresAt"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &patch); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}

	node, _, err := h.Repo.GetProviderNodeByID(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if node == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "provider node not found")
		return
	}

	ctx := r.Context()
	current, err := h.Repo.GetProviderLimits(ctx, id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	merged := current
	pairs := []struct {
		field  string
		patch  *int
		target *int
	}{
		{"rpm", patch.RPM, &merged.RPM},
		{"tpm", patch.TPM, &merged.TPM},
		{"concurrency", patch.Concurrency, &merged.Concurrency},
		{"tokenQuota", patch.TokenQuota, &merged.TokenQuota},
		{"queueTimeoutMs", patch.QueueTimeoutMs, &merged.QueueTimeoutMs},
	}
	for _, p := range pairs {
		v, err := mergeLimit(p.patch, p.target)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, p.field+": "+err.Error())
			return
		}
		*p.target = v
	}

	if patch.ContractExpiresAt != nil {
		expires, err := normalizeContractExpiry(*patch.ContractExpiresAt)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "contractExpiresAt: "+err.Error())
			return
		}
		merged.ContractExpiresAt = expires
	}

	if err := h.Repo.SetProviderLimits(ctx, id, merged); err != nil {
		if errors.Is(err, db.ErrNoSuchProviderNode) {
			handlerutil.WriteJSONError(w, http.StatusNotFound, "provider node not found")
			return
		}
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	res, err := h.providerNodeLimits(ctx, id, merged)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"id":     id,
		"limits": res,
	})
}

// normalizeContractExpiry validates the operator's timestamp and stores it in the
// canonical shape the dispatch gate parses (RFC3339 UTC, or "" for no expiry).
//
// A date-only value is accepted because that is what a date input sends; storing
// it verbatim would be a value Expired() cannot parse, and an unreadable timestamp
// reads as "not expired", which is exactly the wrong answer for a date the
// operator picked.
func normalizeContractExpiry(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC().Format(time.RFC3339), nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return "", errors.New("must be RFC3339 (2026-01-31T00:00:00Z) or a date (2026-01-31)")
	}
	return parsed.UTC().Format(time.RFC3339), nil
}

// HandleResetProviderNodeQuota handles POST /api/provider-nodes/{id}/quota/reset.
//
// The reset moves the node's measurement mark to the end of its history rather
// than zeroing a counter, so the analytics reads behind it stay intact and no
// request can slip its usage onto the wrong side of the boundary. The response
// therefore reports usedTokens as zero while the history still holds the spend.
func (h *DashboardHandler) HandleResetProviderNodeQuota(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing provider node id")
		return
	}

	ctx := r.Context()
	mark, err := h.Repo.ResetProviderQuota(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNoSuchProviderNode) {
			handlerutil.WriteJSONError(w, http.StatusNotFound, "provider node not found")
			return
		}
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	limits, err := h.Repo.GetProviderLimits(ctx, id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status":       "ok",
		"id":           id,
		"quotaResetId": mark,
		"limits":       h.providerNodeLimitsFrom(id, limits, 0),
	})
}
