package dashboard

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/samber/lo"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// HandleGetConnections handles GET /api/connections.
// Returns a JSON list of all connections with secrets stripped: connection
// `data` blobs carry apiKey/accessToken/refreshToken/authToken, and this
// endpoint is reachable with a low-privilege client API key — full rows must
// never leave the server (same sanitizer as the paged providers endpoint).
func (h *DashboardHandler) HandleGetConnections(w http.ResponseWriter, r *http.Request) {
	conns, err := h.Repo.GetProviderConnections("", false)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sanitized := make([]map[string]any, 0, len(conns))
	for _, c := range conns {
		if c == nil {
			continue
		}
		sanitized = append(sanitized, sanitizeProviderConnection(c))
	}
	handlerutil.WriteJSON(w, http.StatusOK, sanitized)
}

// HandleGetProvidersClient handles GET /api/providers and GET /api/providers/client.
// Mirrors upstream 9router providers/client route: usage-eligible connections
// only, with provider/accountStatus filters, priority|provider sort, and
// page/pageSize pagination plus providerOptions and totals.
func (h *DashboardHandler) HandleGetProvidersClient(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	providerFilter := q.Get("provider")
	if providerFilter == "" {
		providerFilter = "all"
	}
	accountStatus := q.Get("accountStatus")
	if accountStatus == "" {
		accountStatus = "all"
	}
	sortMode := q.Get("sort")
	if sortMode == "" {
		sortMode = "priority"
	}
	page := parsePositiveIntQuery(q.Get("page"), 1)
	pageSize := parsePositiveIntQuery(q.Get("pageSize"), defaultProvidersPageSize)
	if pageSize > maxProvidersPageSize {
		pageSize = maxProvidersPageSize
	}

	conns, err := h.Repo.GetProviderConnections("", false)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	eligible := make([]*models.ProviderConnection, 0, len(conns))
	for _, c := range conns {
		if c != nil && isUsageEligibleConnection(c) {
			eligible = append(eligible, c)
		}
	}
	providerOptions := uniqueSortedProviders(eligible)
	providerFiltered := make([]*models.ProviderConnection, 0, len(eligible))
	for _, c := range eligible {
		if providerFilter == "all" || c.Provider == providerFilter {
			providerFiltered = append(providerFiltered, c)
		}
	}
	accountFiltered := make([]*models.ProviderConnection, 0, len(providerFiltered))
	for _, c := range providerFiltered {
		active := c.IsActive != 0
		if accountStatus == "active" && !active {
			continue
		}
		if accountStatus == "inactive" && active {
			continue
		}
		accountFiltered = append(accountFiltered, c)
	}
	sortProviderConnections(accountFiltered, sortMode)

	total := len(accountFiltered)
	totalPages := 1
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	currentPage := page
	if currentPage > totalPages {
		currentPage = totalPages
	}
	start := (currentPage - 1) * pageSize
	end := start + pageSize
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	pageConns := accountFiltered[start:end]
	sanitized := make([]map[string]any, 0, len(pageConns))
	for _, c := range pageConns {
		sanitized = append(sanitized, sanitizeProviderConnection(c))
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"connections":     sanitized,
		"providerOptions": providerOptions,
		"pagination": map[string]any{
			"page": currentPage, "pageSize": pageSize, "total": total, "totalPages": totalPages,
		},
		"totals": map[string]any{
			"eligibleConnections": len(eligible), "providerFilteredConnections": len(providerFiltered),
		},
	})
}

const (
	defaultProvidersPageSize = 20
	maxProvidersPageSize     = 500
)

// usageSupportedProviders mirrors upstream USAGE_SUPPORTED_PROVIDERS
// (registry features.usage).
var usageSupportedProviders = []string{
	"antigravity", "claude", "codebuddy-cn", "codebuddy-intl", "codex",
	"commandcode", "deepseek", "gemini-cli", "github", "glm", "glm-cn",
	"grok-cli", "groq", "kimi", "kiro", "minimax", "minimax-cn", "ollama",
	"opencode-go", "opencode-zen", "qoder", "qoder-cn", "trae",
	"vercel-ai-gateway", "xiaomi-mimo", "zed",
}

// usageApikeyProviders mirrors upstream USAGE_APIKEY_PROVIDERS
// (registry features.usageApikey).
var usageApikeyProviders = []string{
	"codebuddy-cn", "codebuddy-intl", "commandcode", "deepseek", "glm",
	"glm-cn", "groq", "kimi", "kiro", "minimax", "minimax-cn", "ollama",
	"opencode-go", "opencode-zen", "qoder", "qoder-cn", "vercel-ai-gateway",
	"xiaomi-mimo",
}

func strSliceContains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func isUsageEligibleConnection(c *models.ProviderConnection) bool {
	if !strSliceContains(usageSupportedProviders, c.Provider) {
		return false
	}
	return c.AuthType == "oauth" || strSliceContains(usageApikeyProviders, c.Provider)
}

func uniqueSortedProviders(conns []*models.ProviderConnection) []string {
	set := map[string]struct{}{}
	for _, c := range conns {
		set[c.Provider] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func sortProviderConnections(conns []*models.ProviderConnection, sortMode string) {
	order := map[string]int{}
	for i, p := range usageSupportedProviders {
		order[p] = i
	}
	priorityOf := func(c *models.ProviderConnection) int {
		if c.Priority != nil {
			return *c.Priority
		}
		return math.MaxInt
	}
	sort.SliceStable(conns, func(i, j int) bool {
		a, b := conns[i], conns[j]
		if sortMode == "provider" {
			oa, oka := order[a.Provider]
			if !oka {
				oa = len(order)
			}
			ob, okb := order[b.Provider]
			if !okb {
				ob = len(order)
			}
			if oa != ob {
				return oa < ob
			}
			return a.Provider < b.Provider
		}
		if pa, pb := priorityOf(a), priorityOf(b); pa != pb {
			return pa < pb
		}
		return a.Provider < b.Provider
	})
}

func parsePositiveIntQuery(v string, fallback int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
		return n
	}
	return fallback
}

var longTokenPattern = regexp.MustCompile(`[A-Za-z0-9_-]{32,}`)

// maskConnectionName mirrors upstream maskName: long token-like names are truncated.
func maskConnectionName(name string) string {
	if len(name) > 16 && longTokenPattern.MatchString(name) {
		return name[:8] + "***"
	}
	return name
}

// maskConnectionSecret renders a stored credential as a non-reversible hint for
// the edit-connection form (issue #154): enough to confirm *which* key is on
// file, never enough to replay it. Same shape as maskClientKey, which the
// dashboard already uses for client API keys.
func maskConnectionSecret(secret string) string {
	if len(secret) <= 12 {
		return "***"
	}
	return secret[:6] + "…" + secret[len(secret)-4:]
}

// connectionSecretHint returns the credential a connection actually
// authenticates with, in the precedence the runtime uses: the OAuth access
// token first (falling back to the mirrored apiKey), then the plain apiKey.
func connectionSecretHint(data map[string]any) string {
	for _, field := range []string{"accessToken", "apiKey"} {
		if v, ok := data[field].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// sanitizeProviderConnection mirrors upstream sanitize(): only safe fields,
// secrets in data JSON never leave the server.
func sanitizeProviderConnection(c *models.ProviderConnection) map[string]any {

	safe := map[string]any{
		"id": c.ID, "provider": c.Provider, "authType": c.AuthType,
		"isActive": c.IsActive, "createdAt": c.CreatedAt, "updatedAt": c.UpdatedAt,
	}
	if c.Name != nil {
		safe["name"] = maskConnectionName(*c.Name)
	}
	if c.Email != nil {
		safe["email"] = *c.Email
	}
	if c.Priority != nil {
		safe["priority"] = *c.Priority
	}
	if strings.TrimSpace(c.Data) == "" {
		return safe
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(c.Data), &data); err != nil || data == nil {
		return safe
	}

	// Issue #154: the edit-connection form needs to show which credential is on
	// file so a blank field can mean "keep the current one". accessToken wins
	// where present, because that is the credential an OAuth connection
	// actually sends and usage_credentials.go falls back only the other way.
	// Only the mask leaves the server — the raw secret stays dropped.
	if secret := connectionSecretHint(data); secret != "" {
		safe["apiKeyMasked"] = maskConnectionSecret(secret)
	}
	for _, f := range []string{
		"displayName", "defaultModel", "testStatus", "lastError", "lastErrorAt",
		"errorCode", "expiresAt", "lastUsedAt", "consecutiveUseCount",
		"globalPriority", "plan", "message",
		// Operational (non-secret) state the dashboard renders: cooldown
		// badges (⏱), quota/rate-limit panels, model assignment. Secret
		// keys (apiKey/accessToken/refreshToken/authToken/...) stay dropped.
		"backoffLevel", "rateLimitedUntil", "rateLimit", "rateLimitsByModel",
		"freebuffModel", "assignedModel", "freebucks",
	} {
		if v, ok := data[f]; ok && v != nil {
			if f == "lastError" {
				if m, isMap := v.(map[string]any); isMap {
					if msg, ok := m["message"].(string); ok && msg != "" {
						safe[f] = msg
					} else if errText, ok := m["error"].(string); ok && errText != "" {
						safe[f] = errText
					} else if b, err := json.Marshal(m); err == nil {
						safe[f] = string(b)
					} else {
						safe[f] = fmt.Sprintf("%v", v)
					}
				} else if s, isStr := v.(string); isStr {
					safe[f] = s
				} else {
					safe[f] = fmt.Sprintf("%v", v)
				}
			} else {
				safe[f] = v
			}
		}
	}
	for k, v := range data {
		if strings.HasPrefix(k, "modelLock_") && v != nil {
			safe[k] = v
		}
	}
	if psd, ok := data["providerSpecificData"].(map[string]any); ok {
		out := map[string]any{}
		for _, f := range []string{
			"baseUrl", "azureEndpoint", "deployment", "apiVersion", "accountId",
			"region", "projectId", "resourceUrl", "proxyPoolId",
			"connectionProxyEnabled", "connectionProxyUrl", "connectionNoProxy",
			"githubLogin", "githubName", "githubEmail", "githubUserId",
			"username", "firstName", "lastName", "authMethod", "authKind",
			"profileArn",
		} {
			if v, ok := psd[f]; ok && v != nil {
				out[f] = v
			}
		}
		if len(out) > 0 {
			safe["providerSpecificData"] = out
		}
	}
	return safe
}

// createConnectionRequest is the POST /api/connections body. Beyond the legacy
// id/provider/authType/name/apiKey/data fields it accepts what the dashboard
// add-key modal sends, mirroring upstream POST /api/providers: priority,
// providerSpecificData, testStatus and proxyPoolId.
type createConnectionRequest struct {
	ID                   string         `json:"id"`
	Provider             string         `json:"provider"`
	AuthType             string         `json:"authType"`
	Name                 string         `json:"name"`
	DisplayName          string         `json:"displayName"`
	APIKey               string         `json:"apiKey"`
	TestStatus           string         `json:"testStatus"`
	ProxyPoolID          string         `json:"proxyPoolId"`
	Priority             *int           `json:"priority"`
	DefaultModel         string         `json:"defaultModel"`
	ProviderSpecificData map[string]any `json:"providerSpecificData"`
	Data                 any            `json:"data"`
	// AllowOverwrite (or its legacy `overwrite` spelling) states that a
	// name collision is intended. A request carrying an id is already an
	// explicit edit and never needs the flag.
	AllowOverwrite bool `json:"allowOverwrite"`
	Overwrite      bool `json:"overwrite"`
}

// HandleCreateConnection handles POST /api/connections.
// Builds the connection data payload (apiKey + providerSpecificData +
// testStatus + proxyPoolId) and inserts the row with an explicit priority.
func (h *DashboardHandler) HandleCreateConnection(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req createConnectionRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}

	if req.Provider == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing provider")
		return
	}
	// An id in the body means the caller is editing a known connection; its
	// absence is what makes this a create, and a create must not quietly
	// replace an existing one.
	explicitID := req.ID != ""
	if !explicitID {
		req.ID = uuid.New().String()
	}
	if req.AuthType == "" {
		req.AuthType = "apikey"
	}
	name := req.Name
	if name == "" {
		name = req.DisplayName
	}

	dataMap, rawData := decodeConnectionData(req.Data)
	if dataMap == nil {
		dataMap = make(map[string]any)
	}

	apiKey := req.APIKey
	if apiKey == "" {
		if k, ok := dataMap["apiKey"].(string); ok {
			apiKey = k
		}
	}
	if apiKey != "" {
		dataMap["apiKey"] = apiKey
	}
	if len(req.ProviderSpecificData) > 0 {
		dataMap["providerSpecificData"] = req.ProviderSpecificData
	}
	if req.TestStatus != "" {
		dataMap["testStatus"] = req.TestStatus
	}
	if req.ProxyPoolID != "" {
		dataMap["proxyPoolId"] = req.ProxyPoolID
	}
	// Upstream POST /api/providers stores the compatible node's default model
	// alongside the key (AddApiKeyModal sends defaultModel for compatible nodes).
	if req.DefaultModel != "" {
		dataMap["defaultModel"] = req.DefaultModel
	}

	dataStr := rawData
	if len(dataMap) > 0 {
		encoded, err := json.Marshal(dataMap)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to encode connection data")
			return
		}
		dataStr = string(encoded)
	}

	// A create that reuses a name that is already in use used to replace the
	// stored key without a word: a script naming rows "Key 1", "Key 2", … kept
	// the existing pool entries and handed back a success. A caller that means
	// "change the key behind this name" passes an id or an explicit overwrite;
	// everyone else gets a typed 409 naming the row that would have been
	// replaced. #4311
	if !explicitID && req.AuthType == "apikey" && name != "" {
		existing, err := h.Repo.GetProviderConnectionByName(req.Provider, req.AuthType, name)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if existing != nil {
			if !req.AllowOverwrite && !req.Overwrite {
				handlerutil.WriteJSON(w, http.StatusConflict, map[string]any{
					"error": "A connection named \"" + name + "\" already exists for provider \"" +
						req.Provider + "\". Pass allowOverwrite: true to replace it.",
					"code":         "PROVIDER_NAME_CONFLICT",
					"existingId":   existing.ID,
					"existingName": existing.Name,
				})
				return
			}
			// Overwriting rewrites the row in place, keeping its id and its
			// place in the rotation; adding a second row with the same name would
			// leave the account picker with two candidates it cannot tell apart.
			if err := h.Repo.ReplaceProviderConnectionPayload(existing.ID, name, dataStr); err != nil {
				handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
				"status":   "ok",
				"id":       existing.ID,
				"replaced": existing.ID,
			})
			return
		}
	}

	if err := h.Repo.CreateProviderConnectionFull(req.ID, req.Provider, req.AuthType, name, req.Priority, dataStr); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"id":     req.ID,
	})
}

// decodeConnectionData normalises the legacy `data` field into a mutable map
// plus the raw string form for payloads that are not a JSON object (those are
// stored verbatim, as before).
func decodeConnectionData(data any) (map[string]any, string) {
	switch d := data.(type) {
	case nil:
		return nil, ""
	case string:
		var m map[string]any
		if err := json.Unmarshal([]byte(d), &m); err == nil {
			return m, ""
		}
		return nil, d
	case map[string]any:
		return d, ""
	default:
		encoded, err := json.Marshal(d)
		if err != nil {
			return nil, ""
		}
		var m map[string]any
		if err := json.Unmarshal(encoded, &m); err == nil {
			return m, ""
		}
		return nil, string(encoded)
	}
}

// HandleUpdateConnection handles PUT /api/connections/{id}.
// Parses name, priority, isActive, data and calls UpdateProviderConnection or SetConnectionStatus.
func (h *DashboardHandler) HandleUpdateConnection(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing connection id")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var rawBody map[string]any
	if err := json.Unmarshal(body, &rawBody); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	existing, err := h.Repo.GetProviderConnectionByID(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "connection not found")
		return
	}

	_, hasName := rawBody["name"]
	_, hasPriority := rawBody["priority"]
	_, hasData := rawBody["data"]
	_, hasPSD := rawBody["providerSpecificData"]
	_, hasAssignedModel := rawBody["assignedModel"]
	_, hasProxyPool := rawBody["proxyPoolId"]
	// Issue #154: rotating the credential from the edit modal. Blank (or
	// whitespace-only) means "keep the stored key" — the modal shows it as a
	// mask and sends nothing when the user did not type one.
	newAPIKey := ""
	if s, ok := rawBody["apiKey"].(string); ok {
		newAPIKey = strings.TrimSpace(s)
	}
	hasAPIKey := newAPIKey != ""
	a, hasIsActive := rawBody["isActive"].(bool)

	// Upstream normalizeProxyPoolUpdate: null/""/"__none__" unbinds, anything
	// else must reference an existing pool.
	proxyPoolID := ""
	unbindProxyPool := false
	if hasProxyPool {
		switch v := rawBody["proxyPoolId"].(type) {
		case nil:
			unbindProxyPool = true
		case string:
			trimmed := strings.TrimSpace(v)
			if trimmed == "" || trimmed == "__none__" {
				unbindProxyPool = true
			} else {
				proxyPoolID = trimmed
			}
		default:
			unbindProxyPool = true
		}
		if proxyPoolID != "" {
			pool, perr := h.Repo.GetProxyPool(proxyPoolID)
			if perr != nil || pool == nil {
				handlerutil.WriteJSONError(w, http.StatusBadRequest, "Proxy pool not found")
				return
			}
		}
	}

	// Fast path: if only updating active status
	if hasIsActive && !hasName && !hasPriority && !hasData && !hasPSD && !hasAssignedModel && !hasProxyPool && !hasAPIKey {
		if err := h.Repo.SetConnectionStatus(id, a); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id, "isActive": a})
		return
	}

	name := ""
	if n, ok := rawBody["name"].(string); ok && n != "" {
		name = n
	} else if existing.Name != nil {
		name = *existing.Name
	}
	// A NULL priority must stay NULL. Defaulting it to 0 would sort this row
	// ahead of every other account (0 beats every positive rank) and silently
	// promote it to the top of the rotation on any unrelated edit — rename,
	// proxy assignment, model assignment.
	priority := existing.Priority
	if p, ok := rawBody["priority"].(float64); ok {
		priority = lo.ToPtr(int(p))
	}
	isActive := existing.IsActive == 1
	if hasIsActive {
		isActive = a
	}

	dataStr := existing.Data
	if hasData || hasPSD || hasAssignedModel || hasProxyPool || hasAPIKey {
		dataMap := make(map[string]any)
		if existing.Data != "" {
			_ = json.Unmarshal([]byte(existing.Data), &dataMap)
		}

		if hasData && rawBody["data"] != nil {
			switch d := rawBody["data"].(type) {
			case string:
				var m map[string]any
				if err := json.Unmarshal([]byte(d), &m); err == nil {
					for k, v := range m {
						if k == "providerSpecificData" {
							if psd, ok := v.(map[string]any); ok {
								mergeMapField(dataMap, "providerSpecificData", psd)
								continue
							}
						}
						dataMap[k] = v
					}
				} else {
					dataStr = d
				}
			case map[string]any:
				for k, v := range d {
					if k == "providerSpecificData" {
						if psd, ok := v.(map[string]any); ok {
							mergeMapField(dataMap, "providerSpecificData", psd)
							continue
						}
					}
					dataMap[k] = v
				}
			}
		}

		// Issue #154: credential rotation. Only the key itself is replaced —
		// testStatus/lastError stay whatever the last probe decided. The modal
		// clears them explicitly once it has validated the new key against the
		// provider, which is the only evidence that justifies marking the row
		// active again (upstream EditConnectionModal sends testStatus).
		if hasAPIKey {
			dataMap["apiKey"] = newAPIKey
		}

		if hasPSD {
			if psd, ok := rawBody["providerSpecificData"].(map[string]any); ok {
				mergeMapField(dataMap, "providerSpecificData", psd)
			}
		}

		if hasAssignedModel {
			if am, ok := rawBody["assignedModel"].(string); ok {
				dataMap["assignedModel"] = am
			} else if rawBody["assignedModel"] == nil {
				delete(dataMap, "assignedModel")
			}
		}

		if hasProxyPool {
			// Write both locations: upstream keeps the binding in
			// providerSpecificData.proxyPoolId, while this backend's chat
			// resolver reads the top-level proxyPoolId.
			if unbindProxyPool {
				delete(dataMap, "proxyPoolId")
				if psd, ok := dataMap["providerSpecificData"].(map[string]any); ok {
					delete(psd, "proxyPoolId")
				}
			} else {
				dataMap["proxyPoolId"] = proxyPoolID
				mergeMapField(dataMap, "providerSpecificData", map[string]any{"proxyPoolId": proxyPoolID})
			}
		}

		if b, err := json.Marshal(dataMap); err == nil {
			dataStr = string(b)
		}
	}

	if err := h.Repo.UpdateProviderConnection(id, name, priority, isActive, dataStr); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
}

// HandleReorderConnection handles POST /api/connections/{id}/reorder.
// Body: {"direction": "up"|"down"}.
//
// The whole swap plus the 1..N renumbering happens in one SQLite transaction,
// so the pool can never end up with two rows sharing a priority. See
// Repo.ReorderProviderConnections for why the previous two-PUT client swap was
// not safe.
func (h *DashboardHandler) HandleReorderConnection(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing connection id")
		return
	}

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	var body struct {
		Direction string `json:"direction"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	direction := 0
	switch strings.ToLower(strings.TrimSpace(body.Direction)) {
	case "up":
		direction = -1
	case "down":
		direction = 1
	default:
		handlerutil.WriteJSONError(w, http.StatusBadRequest, `direction must be "up" or "down"`)
		return
	}

	conn, err := h.Repo.GetProviderConnectionByID(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conn == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "connection not found")
		return
	}

	if err := h.Repo.ReorderProviderConnections(conn.Provider, id, direction); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	conns, err := h.Repo.GetProviderConnections(conn.Provider, false)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id, "connections": conns})
}

func mergeMapField(target map[string]any, key string, source map[string]any) {
	existing, _ := target[key].(map[string]any)
	if existing == nil {
		existing = make(map[string]any)
	}
	for k, v := range source {
		existing[k] = v
	}
	target[key] = existing
}

// HandleDeleteConnection handles DELETE /api/connections/{id}.
// Calls DeleteProviderConnection.
func (h *DashboardHandler) HandleDeleteConnection(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing connection id")
		return
	}

	if err := h.Repo.DeleteProviderConnection(id); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
}

// HandleGetConnectionModels handles GET /api/providers/{id}/models.
// Mirrors upstream src/app/api/providers/[id]/models/route.js for
// OpenAI/Anthropic-compatible connections: the id is a connection id whose
// provider must be an openai-compatible-*/anthropic-compatible-* node. The
// node baseUrl is probed for GET /models (upstream parity: OpenAI uses
// Bearer, Anthropic strips a /messages suffix and sends x-api-key +
// anthropic-version plus Bearer). Other providers answer 400.
func (h *DashboardHandler) HandleGetConnectionModels(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing connection id")
		return
	}
	h.writeConnectionModels(w, r, id)
}

// writeConnectionModels is HandleGetConnectionModels with the connection id
// already resolved. The split exists because model sync (POST
// /api/models/sync) needs the same discovery for a connection it selected
// itself, and a second copy of this provider-by-provider chain would be free
// to drift from the one the dashboard imports models with (#179).
func (h *DashboardHandler) writeConnectionModels(w http.ResponseWriter, r *http.Request, id string) {
	conn, err := h.Repo.GetProviderConnectionByID(id)
	if err != nil || conn == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "connection not found")
		return
	}
	var connData struct {
		APIKey               string         `json:"apiKey"`
		AccessToken          string         `json:"accessToken"`
		RefreshToken         string         `json:"refreshToken"`
		ProviderSpecificData map[string]any `json:"providerSpecificData"`
	}
	if conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &connData)
	}

	type modelItem struct {
		ID           string          `json:"id"`
		Name         string          `json:"name"`
		Capabilities map[string]bool `json:"capabilities,omitempty"`
	}

	if conn.Provider == "antigravity" {
		token := connData.AccessToken
		if token == "" {
			token = connData.APIKey
		}
		if token == "" {
			handlerutil.WriteJSONError(w, http.StatusUnauthorized, "no valid token found")
			return
		}
		projectID := "antigravity"
		if connData.ProviderSpecificData != nil {
			if p, ok := connData.ProviderSpecificData["projectId"].(string); ok && p != "" {
				projectID = p
			}
		}
		reqPayload, _ := json.Marshal(map[string]any{"project": projectID})
		headers := map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + token,
			"User-Agent":    "antigravity/ide/2.11.0 darwin/arm64",
		}
		status, body, err := validateProbeDo(r.Context(), http.MethodPost, "https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels", headers, reqPayload)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to fetch antigravity models: "+err.Error())
			return
		}
		if status != http.StatusOK {
			handlerutil.WriteJSONError(w, status, fmt.Sprintf("failed to fetch models: %d", status))
			return
		}
		var agResp struct {
			Models map[string]struct {
				DisplayName      string `json:"displayName"`
				SupportsImages   bool   `json:"supportsImages"`
				SupportsThinking bool   `json:"supportsThinking"`
				IsInternal       bool   `json:"isInternal"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &agResp); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, "invalid JSON from antigravity models")
			return
		}
		var modelsList []modelItem
		for k, m := range agResp.Models {
			if m.IsInternal || strings.HasPrefix(k, "chat_") || strings.HasPrefix(k, "tab_") {
				continue
			}
			displayName := m.DisplayName
			if displayName == "" {
				displayName = k
			}
			modelsList = append(modelsList, modelItem{
				ID:   k,
				Name: displayName,
				Capabilities: map[string]bool{
					"vision":    m.SupportsImages,
					"reasoning": m.SupportsThinking,
				},
			})
		}
		sort.Slice(modelsList, func(i, j int) bool {
			return modelsList[i].ID < modelsList[j].ID
		})
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"provider":     conn.Provider,
			"connectionId": conn.ID,
			"models":       modelsList,
		})
		return
	}

	if conn.Provider == "gemini-cli" {
		token := connData.AccessToken
		if token == "" {
			token = connData.APIKey
		}
		if token == "" {
			handlerutil.WriteJSONError(w, http.StatusUnauthorized, "no valid token found")
			return
		}
		projectID := ""
		if connData.ProviderSpecificData != nil {
			if p, ok := connData.ProviderSpecificData["projectId"].(string); ok && p != "" {
				projectID = p
			}
		}
		reqPayload, _ := json.Marshal(map[string]any{"project": projectID})
		headers := map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + token,
		}
		status, body, err := validateProbeDo(r.Context(), http.MethodPost, "https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels", headers, reqPayload)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to fetch gemini-cli models: "+err.Error())
			return
		}
		if status != http.StatusOK {
			handlerutil.WriteJSONError(w, status, fmt.Sprintf("failed to fetch models: %d", status))
			return
		}
		var resp struct {
			Models map[string]struct {
				DisplayName      string `json:"displayName"`
				SupportsImages   bool   `json:"supportsImages"`
				SupportsThinking bool   `json:"supportsThinking"`
				IsInternal       bool   `json:"isInternal"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, "invalid JSON from gemini-cli models")
			return
		}
		var modelsList []modelItem
		for k, m := range resp.Models {
			if m.IsInternal || strings.HasPrefix(k, "chat_") || strings.HasPrefix(k, "tab_") {
				continue
			}
			displayName := m.DisplayName
			if displayName == "" {
				displayName = k
			}
			modelsList = append(modelsList, modelItem{
				ID:   k,
				Name: displayName,
				Capabilities: map[string]bool{
					"vision":    m.SupportsImages,
					"reasoning": m.SupportsThinking,
				},
			})
		}
		sort.Slice(modelsList, func(i, j int) bool {
			return modelsList[i].ID < modelsList[j].ID
		})
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"provider":     conn.Provider,
			"connectionId": conn.ID,
			"models":       modelsList,
		})
		return
	}

	if conn.Provider == "cline" || conn.Provider == "clinepass" {
		token := connData.AccessToken
		if token == "" {
			token = connData.APIKey
		}
		if token == "" {
			handlerutil.WriteJSONError(w, http.StatusUnauthorized, "no valid token found")
			return
		}
		authHeaderVal := token
		if strings.HasPrefix(token, "eyJ") {
			authHeaderVal = "workos:" + token
		}
		headers := map[string]string{
			"Authorization": "Bearer " + authHeaderVal,
			"User-Agent":    "Cline/3.0.0",
		}
		status, body, err := validateProbeDo(r.Context(), http.MethodGet, "https://api.cline.bot/api/v1/models", headers, nil)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to fetch cline models: "+err.Error())
			return
		}
		if status != http.StatusOK {
			handlerutil.WriteJSONError(w, status, fmt.Sprintf("failed to fetch models: %d", status))
			return
		}
		var clineResp struct {
			Data   []any `json:"data"`
			Models []any `json:"models"`
		}
		_ = json.Unmarshal(body, &clineResp)
		models := clineResp.Data
		if len(models) == 0 {
			models = clineResp.Models
		}

		// The free tier is published on a separate feed: /api/v1/models carries
		// no `cline-free/*` ids at all. The feed is additive and a dead feed must
		// never take the catalogue down with it, so ids already present win —
		// the same "first writer wins" rule upstream uses.
		models = mergeClineFreeTier(models, clineFreeTierModels(r.Context()))
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"provider":     conn.Provider,
			"connectionId": conn.ID,
			"models":       models,
		})
		return
	}
	// Qoder's catalogue lives behind a COSY-signed gateway endpoint rather than
	// a plain /models URL, so it needs its own branch.
	if conn.Provider == "qoder" || conn.Provider == "qoder-cn" {
		token := connData.APIKey
		if token == "" {
			token = connData.AccessToken
		}
		qoderModels, err := fetchQoderCatalogModels(r.Context(), conn.Provider, token, connData.ProviderSpecificData)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to fetch Qoder models: "+err.Error())
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"provider":     conn.Provider,
			"connectionId": conn.ID,
			"models":       qoderModels,
		})
		return
	}

	// OpenAI-compatible aggregators with a live /v1/models catalogue. The
	// connection's key is the only credential these need.
	if listURL := providers.ModelsListURL(conn.Provider); listURL != "" {
		token := connData.APIKey
		if token == "" {
			token = connData.AccessToken
		}
		if token == "" {
			handlerutil.WriteJSONError(w, http.StatusUnauthorized, "no API key configured")
			return
		}
		headers := map[string]string{"Authorization": "Bearer " + token}
		for k, v := range providers.ModelsListHeaders(conn.Provider) {
			headers[k] = v
		}
		status, body, err := validateProbeDo(r.Context(), http.MethodGet, listURL, headers, nil)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to fetch models: "+err.Error())
			return
		}
		if status != http.StatusOK {
			handlerutil.WriteJSONError(w, status, fmt.Sprintf("failed to fetch models: %d", status))
			return
		}
		var listResp struct {
			Data   []any `json:"data"`
			Models []any `json:"models"`
		}
		_ = json.Unmarshal(body, &listResp)
		items := listResp.Data
		if len(items) == 0 {
			items = listResp.Models
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"provider":     conn.Provider,
			"connectionId": conn.ID,
			"models":       items,
		})
		return
	}

	isOpenAI := strings.HasPrefix(conn.Provider, "openai-compatible-")
	isAnthropic := strings.HasPrefix(conn.Provider, "anthropic-compatible-")
	if !isOpenAI && !isAnthropic {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "provider "+conn.Provider+" does not support models listing")
		return
	}
	baseURL := ""
	if connData.ProviderSpecificData != nil {
		if v, ok := connData.ProviderSpecificData["baseUrl"].(string); ok {
			baseURL = strings.TrimSpace(v)
		}
	}
	if baseURL == "" {
		if _, nodeData, nerr := h.Repo.GetProviderNodeByID(conn.Provider); nerr == nil && nodeData != nil {
			baseURL = strings.TrimSpace(nodeData.BaseURL)
		}
	}
	if baseURL == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "no base URL configured for OpenAI compatible provider")
		return
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	headers := map[string]string{"Content-Type": "application/json"}
	if isAnthropic {
		baseURL = strings.TrimSuffix(baseURL, "/messages")
		headers["x-api-key"] = connData.APIKey
		headers["anthropic-version"] = "2023-06-01"
		headers["Authorization"] = "Bearer " + connData.APIKey
	} else {
		headers["Authorization"] = "Bearer " + connData.APIKey
	}
	status, body, err := validateProbeDo(r.Context(), http.MethodGet, baseURL+"/models", headers, nil)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, validateNodeNetworkMessage(err))
		return
	}
	if status != http.StatusOK {
		handlerutil.WriteJSONError(w, status, "failed to fetch models: "+http.StatusText(status))
		return
	}
	var parsed struct {
		Data   []any `json:"data"`
		Models []any `json:"models"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "invalid models response")
		return
	}
	models := parsed.Data
	if models == nil {
		models = parsed.Models
	}
	if models == nil {
		models = []any{}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider":     conn.Provider,
		"connectionId": conn.ID,
		"models":       models,
	})
}

// clineFreeTierEndpoint publishes Cline's free tier. The main /api/v1/models
// catalogue carries no `cline-free/*` ids, so the tier has to come from here.
// Upstream: open-sse/services/clinepassModels.js (v0.5.91).
const clineFreeTierEndpoint = "https://api.cline.bot/api/v1/ai/cline/recommended-models"

// clineFreeTierModels returns the recommended-models feed's free[] tier, or nil
// on any failure — the tier is additive, so a dead feed must not break the
// catalogue the caller already has.
func clineFreeTierModels(ctx context.Context) []any {
	status, body, err := validateProbeDo(
		ctx, http.MethodGet, clineFreeTierEndpoint,
		map[string]string{"Accept": "application/json"}, nil,
	)
	if err != nil || status != http.StatusOK {
		return nil
	}
	var feed struct {
		Free []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"free"`
	}
	if err := json.Unmarshal(body, &feed); err != nil {
		return nil
	}
	out := make([]any, 0, len(feed.Free))
	for _, m := range feed.Free {
		if strings.TrimSpace(m.ID) == "" {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, map[string]any{"id": m.ID, "name": name})
	}
	return out
}

// mergeClineFreeTier appends the free-tier entries the catalogue is missing.
// First writer wins on a shared id, so anything the two sources agree on keeps
// the catalogue's own entry.
func mergeClineFreeTier(catalogue, free []any) []any {
	if len(free) == 0 {
		return catalogue
	}
	present := make(map[string]bool, len(catalogue))
	for _, m := range catalogue {
		if mm, ok := m.(map[string]any); ok {
			if id, ok := mm["id"].(string); ok {
				present[id] = true
			}
		}
	}
	for _, m := range free {
		mm, _ := m.(map[string]any)
		id, _ := mm["id"].(string)
		if !present[id] {
			catalogue = append(catalogue, m)
		}
	}
	return catalogue
}
