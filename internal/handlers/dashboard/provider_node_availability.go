package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// ProviderNodeAvailabilityStatus is the state shown below the prefix and
// suffix fields while an operator names a compatible provider.
type ProviderNodeAvailabilityStatus string

const (
	ProviderNodeAvailabilityAvailable ProviderNodeAvailabilityStatus = "available"
	ProviderNodeAvailabilityTaken     ProviderNodeAvailabilityStatus = "taken"
	ProviderNodeAvailabilityInvalid   ProviderNodeAvailabilityStatus = "invalid"
	ProviderNodeAvailabilityRandom    ProviderNodeAvailabilityStatus = "random"
	ProviderNodeAvailabilityAutomatic ProviderNodeAvailabilityStatus = "automatic"
)

// AvailabilityRoutePath is the path under /api the client calls. Both route
// tables (dashboard/routes.go and handlers/router.go) must register it; the
// test asserts the constant the client's URL is built from matches.
const AvailabilityRoutePath = "/provider-nodes/availability"

// ProviderNodeFieldAvailability is the result for one candidate identifier.
type ProviderNodeFieldAvailability struct {
	Value      string                         `json:"value"`
	Status     ProviderNodeAvailabilityStatus `json:"status"`
	Reason     string                         `json:"reason,omitempty"`
	ProviderID string                         `json:"providerId,omitempty"`
}

// ProviderNodeAvailabilityResponse reports the live status of both identifiers.
type ProviderNodeAvailabilityResponse struct {
	Prefix    ProviderNodeFieldAvailability `json:"prefix"`
	URLSuffix ProviderNodeFieldAvailability `json:"urlSuffix"`
}

// HandleCheckProviderNodeAvailability checks a candidate prefix and URL suffix
// against the same owners the create handler will reject. It is advisory: the
// create route still checks again, because another request can claim a value
// after this response.
func (h *DashboardHandler) HandleCheckProviderNodeAvailability(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	var req struct {
		Prefix    string `json:"prefix"`
		URLSuffix string `json:"urlSuffix"`
		Type      string `json:"type"`
		APIType   string `json:"apiType"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	nodeType := strings.TrimSpace(req.Type)
	if nodeType == "" {
		nodeType = "openai-compatible"
	}
	apiType := strings.TrimSpace(req.APIType)
	if apiType == "" {
		apiType = "chat"
	}
	nodes, err := h.Repo.GetProviderNodes()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "could not check provider names")
		return
	}

	result := ProviderNodeAvailabilityResponse{
		Prefix:    checkProviderNodePrefix(strings.TrimSpace(req.Prefix), nodeType, nodes),
		URLSuffix: checkProviderNodeURLSuffix(strings.TrimSpace(req.URLSuffix), nodeType, apiType, nodes),
	}
	handlerutil.WriteJSON(w, http.StatusOK, result)
}

func checkProviderNodePrefix(prefix, nodeType string, nodes []*models.ProviderNode) ProviderNodeFieldAvailability {
	result := ProviderNodeFieldAvailability{Value: prefix}
	if prefix == "" {
		if nodeType == "custom-embedding" {
			result.Status = ProviderNodeAvailabilityInvalid
			result.Reason = "Prefix is required for custom embedding providers."
			return result
		}
		result.Status = ProviderNodeAvailabilityAutomatic
		result.Reason = "A prefix will be assigned automatically when you save."
		return result
	}
	if providers.IsReservedProviderPrefix(prefix) {
		result.Status = ProviderNodeAvailabilityTaken
		if _, alias := providers.ProviderAliasMap[strings.ToLower(prefix)]; alias {
			result.Reason = "Already used by provider alias \"" + strings.ToLower(prefix) + "\"."
		} else {
			result.Reason = "Already used by provider \"" + strings.ToLower(prefix) + "\"."
		}
		return result
	}
	for _, node := range nodes {
		if node == nil {
			continue
		}
		var data db.ProviderNodeData
		if err := json.Unmarshal([]byte(node.Data), &data); err != nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(data.Prefix), prefix) {
			result.Status = ProviderNodeAvailabilityTaken
			owner := node.ID
			if node.Name != nil && strings.TrimSpace(*node.Name) != "" {
				owner = strings.TrimSpace(*node.Name)
			}
			result.Reason = "Already used by provider node \"" + owner + "\"."
			return result
		}
	}
	result.Status = ProviderNodeAvailabilityAvailable
	return result
}

func checkProviderNodeURLSuffix(suffix, nodeType, apiType string, nodes []*models.ProviderNode) ProviderNodeFieldAvailability {
	result := ProviderNodeFieldAvailability{Value: suffix}
	if suffix == "" {
		result.Status = ProviderNodeAvailabilityRandom
		result.Reason = "A random URL suffix will be assigned when you save."
		return result
	}
	validSuffix, err := validateNodeIDSuffix(suffix)
	if err != nil {
		result.Status = ProviderNodeAvailabilityInvalid
		result.Reason = err.Error()
		return result
	}
	id := composeProviderNodeID(nodeType, apiType, validSuffix)
	result.ProviderID = id
	if len(id) > maxProviderNodeIDLength {
		result.Status = ProviderNodeAvailabilityInvalid
		result.Reason = "The provider id is too long."
		return result
	}
	for _, node := range nodes {
		if node != nil && node.ID == id {
			result.Status = ProviderNodeAvailabilityTaken
			result.Reason = "This provider id is already in use."
			return result
		}
	}
	result.Status = ProviderNodeAvailabilityAvailable
	return result
}
