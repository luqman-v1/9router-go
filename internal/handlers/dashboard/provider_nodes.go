package dashboard

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"

	json "encoding/json/v2"

	"9router/proxy/internal/handlerutil"
)

// ProviderNodeResponse is the JSON representation of a provider node with unpacked data fields.
type ProviderNodeResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Prefix  string `json:"prefix,omitempty"`
	APIType string `json:"apiType,omitempty"`
	BaseURL string `json:"baseUrl,omitempty"`
	// URLSuffix is the custom tail of the provider id, without the
	// "openai-compatible-chat-" literal in front of it. It is empty for a node
	// created before the field existed, whose id ends in a random uuid the
	// dashboard therefore reports as generated rather than as a suffix the
	// user can edit back.
	URLSuffix string `json:"urlSuffix,omitempty"`
	// URLSuffixGenerated marks an id whose tail is the random fallback, so the
	// dashboard can render it read-only instead of offering an edit that would
	// look like it was discarding a value the user never set.
	URLSuffixGenerated bool   `json:"urlSuffixGenerated"`
	CreatedAt          string `json:"createdAt"`
	UpdatedAt          string `json:"updatedAt"`
	// Limits is the node's capacity policy plus the usage it is measured against
	// (PR4). The list fills it for every node so the editor never has to make a
	// second call to discover what a node is currently allowed.
	Limits *ProviderNodeLimitsResponse `json:"limits,omitempty"`
}

// HandleGetProviderNodes handles GET /api/provider-nodes.
func (h *DashboardHandler) HandleGetProviderNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.Repo.GetProviderNodes()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// One query for every policy and one for every usage, rather than a read per
	// node: the list is the dashboard's node page, and a per-node read there
	// would be N+1 on every load.
	policies, err := h.Repo.ListProviderLimits(r.Context())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	usage, err := h.Repo.ProviderTokenUsageByNode(r.Context())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	res := make([]ProviderNodeResponse, 0, len(nodes))
	for _, n := range nodes {
		nodeType := ""
		if n.Type != nil {
			nodeType = *n.Type
		}
		nodeName := ""
		if n.Name != nil {
			nodeName = *n.Name
		}

		item := ProviderNodeResponse{
			ID:        n.ID,
			Type:      nodeType,
			Name:      nodeName,
			CreatedAt: n.CreatedAt,
			UpdatedAt: n.UpdatedAt,
		}

		// The suffix is not a stored column — it is whatever the id carries
		// after the type literal — so the list derives it the same way the
		// create/update handlers composed it.
		item.URLSuffix, item.URLSuffixGenerated = providerNodeSuffixOf(n.ID)
		limits := h.providerNodeLimitsFrom(n.ID, policies[n.ID], usage[n.ID])
		item.Limits = &limits

		if n.Data != "" {
			var dataObj struct {
				Prefix  string `json:"prefix"`
				APIType string `json:"apiType"`
				BaseURL string `json:"baseUrl"`
			}
			if err := json.Unmarshal([]byte(n.Data), &dataObj); err == nil {
				item.Prefix = dataObj.Prefix
				item.APIType = dataObj.APIType
				item.BaseURL = dataObj.BaseURL
			}
		}

		res = append(res, item)
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"nodes": res,
	})
}

// HandleCreateProviderNode handles POST /api/provider-nodes.
//
// Mirrors upstream src/app/api/provider-nodes/route.js, with one addition:
// the id tail is the caller's `urlSuffix` when supplied, so the provider id is
// "openai-compatible-chat-<suffix>" instead of "openai-compatible-chat-<uuid>".
// An empty suffix keeps the random id upstream generates.
func (h *DashboardHandler) HandleCreateProviderNode(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		Name      string `json:"name"`
		Prefix    string `json:"prefix"`
		APIType   string `json:"apiType"`
		BaseURL   string `json:"baseUrl"`
		Type      string `json:"type"`
		URLSuffix string `json:"urlSuffix"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Prefix = strings.TrimSpace(req.Prefix)
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	if req.Name == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Prefix == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "prefix is required")
		return
	}

	nodeType := req.Type
	if nodeType == "" {
		nodeType = "openai-compatible"
	}

	apiType := req.APIType
	if apiType == "" {
		apiType = "chat"
	}

	_, defaultBaseURL := providerNodeLiterals(nodeType, apiType)
	if req.BaseURL == "" {
		req.BaseURL = defaultBaseURL
	}
	id, suffix, ok := h.resolveProviderNodeID(w, nodeType, apiType, req.URLSuffix, "")
	if !ok {
		return
	}

	dataBytes, _ := json.Marshal(map[string]string{
		"prefix":  req.Prefix,
		"apiType": apiType,
		"baseUrl": req.BaseURL,
	})

	node, err := h.Repo.CreateProviderNode(id, nodeType, req.Name, string(dataBytes))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusCreated, map[string]any{
		"node": ProviderNodeResponse{
			ID:                 node.ID,
			Type:               nodeType,
			Name:               req.Name,
			Prefix:             req.Prefix,
			APIType:            apiType,
			BaseURL:            req.BaseURL,
			URLSuffix:          suffix,
			URLSuffixGenerated: suffix == "",
			CreatedAt:          node.CreatedAt,
			UpdatedAt:          node.UpdatedAt,
		},
	})
}

// HandleUpdateProviderNode handles PUT /api/provider-nodes/{id}.
// Mirrors upstream src/app/api/provider-nodes/[id]/route.js: name and prefix
// are required, apiType is validated only for openai-compatible nodes, and the
// base URL is sanitized (strip /messages for anthropic-compatible,
// /embeddings for custom-embedding). Attached connections inherit the new
// prefix/apiType/baseUrl/nodeName into their providerSpecificData.
//
// A changed `urlSuffix` renames the provider id. Upstream cannot do this — its
// id is fixed at creation — so the rename is additive here: the response
// carries the new id and the dashboard navigates to it, because the URL the
// user is looking at stops resolving the moment the row moves.
func (h *DashboardHandler) HandleUpdateProviderNode(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing node id")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()
	var req struct {
		Name      string `json:"name"`
		Prefix    string `json:"prefix"`
		APIType   string `json:"apiType"`
		BaseURL   string `json:"baseUrl"`
		URLSuffix string `json:"urlSuffix"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}
	node, nodeData, err := h.Repo.GetProviderNodeByID(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if node == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "provider node not found")
		return
	}
	name := strings.TrimSpace(req.Name)
	prefix := strings.TrimSpace(req.Prefix)
	baseURL := strings.TrimSpace(req.BaseURL)
	if name == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	if prefix == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "prefix is required")
		return
	}
	nodeType := ""
	if node.Type != nil {
		nodeType = *node.Type
	}
	if nodeType == "openai-compatible" && req.APIType != "chat" && req.APIType != "responses" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid OpenAI compatible API type")
		return
	}
	if baseURL == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "base URL is required")
		return
	}
	if nodeType == "anthropic-compatible" {
		baseURL = strings.TrimSuffix(baseURL, "/")
		baseURL = strings.TrimSuffix(baseURL, "/messages")
	}
	if nodeType == "custom-embedding" {
		baseURL = strings.TrimSuffix(baseURL, "/")
		baseURL = strings.TrimSuffix(baseURL, "/embeddings")
	}
	apiType := ""
	if nodeData != nil {
		apiType = nodeData.APIType
	}
	if nodeType == "openai-compatible" {
		apiType = req.APIType
	}

	// An apiType change does not rename the node. Upstream's id is immutable,
	// so there the two simply drift apart (docs/PARITY-REMAINING.md) and every
	// reader takes the apiType from the data blob, not from the id. Keeping
	// that behaviour also keeps the suffix the user typed across the edit,
	// which is the whole point of the field.
	newID, suffix, ok := h.resolveProviderNodeID(w, nodeType, apiType, req.URLSuffix, id)
	if !ok {
		return
	}
	renamedID, ok := h.applyProviderNodeRename(w, id, newID)
	if !ok {
		return
	}

	dataBytes, _ := json.Marshal(map[string]string{
		"prefix":  prefix,
		"apiType": apiType,
		"baseUrl": baseURL,
	})
	updated, err := h.Repo.UpdateProviderNode(renamedID, name, string(dataBytes))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	syncNodeConnections(h, renamedID, prefix, apiType, nodeType, baseURL, name)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"node": ProviderNodeResponse{
			ID:                 updated.ID,
			Type:               nodeType,
			Name:               name,
			Prefix:             prefix,
			APIType:            apiType,
			BaseURL:            baseURL,
			URLSuffix:          suffix,
			URLSuffixGenerated: suffix == "",
			CreatedAt:          updated.CreatedAt,
			UpdatedAt:          updated.UpdatedAt,
		},
	})
}

// syncNodeConnections propagates edited node fields into the
// providerSpecificData of every attached connection, mirroring upstream
// updateProviderConnection calls after updateProviderNode.
func syncNodeConnections(h *DashboardHandler, nodeID, prefix, apiType, nodeType, baseURL, nodeName string) {
	conns, err := h.Repo.GetProviderConnections(nodeID, false)
	if err != nil || len(conns) == 0 {
		return
	}
	for _, conn := range conns {
		if conn == nil {
			continue
		}
		dataMap := make(map[string]any)
		if conn.Data != "" {
			_ = json.Unmarshal([]byte(conn.Data), &dataMap)
		}
		psd, _ := dataMap["providerSpecificData"].(map[string]any)
		if psd == nil {
			psd = make(map[string]any)
		}
		psd["prefix"] = prefix
		if nodeType == "openai-compatible" {
			psd["apiType"] = apiType
		} else {
			delete(psd, "apiType")
		}
		psd["baseUrl"] = baseURL
		psd["nodeName"] = nodeName
		dataMap["providerSpecificData"] = psd
		dataBytes, err := json.Marshal(dataMap)
		if err != nil {
			continue
		}
		_ = h.Repo.UpdateConnectionData(conn.ID, string(dataBytes))
	}
}

// HandleDeleteProviderNode handles DELETE /api/provider-nodes/{id}.
func (h *DashboardHandler) HandleDeleteProviderNode(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing node id")
		return
	}

	if err := h.Repo.DeleteProviderNode(r.Context(), id); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The node's models stop resolving the moment the row is gone, so every
	// combo a rule put them into must drop them. The candidate builder treats
	// an alias with no node behind it as not live, which is what makes this
	// pass remove exactly those members.
	h.reconcileSmartCombosBestEffort(r.Context(), "provider node deleted")
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
}

// HandleValidateProviderNode handles POST /api/provider-nodes/validate.
// Ports upstream src/app/api/provider-nodes/validate/route.js: probes an
// OpenAI-compatible / Anthropic-compatible base URL with the supplied key and
// answers {valid, error, method, dimensions} so the dashboard Check button can
// show a badge without persisting the key.
func (h *DashboardHandler) HandleValidateProviderNode(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		Type    string `json:"type"`
		ModelID string `json:"modelId"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}

	baseURL := strings.TrimSpace(req.BaseURL)
	apiKey := strings.TrimSpace(req.APIKey)
	if baseURL == "" || apiKey == "" {
		writeNodeValidation(w, map[string]any{"valid": false, "error": "Base URL and API key required"})
		return
	}
	if !isValidHTTPURL(baseURL) {
		writeNodeValidation(w, map[string]any{"valid": false, "error": "Invalid URL format"})
		return
	}

	// Cloud metadata is refused for every caller, including loopback. The
	// local-caller bypass exists so a self-hosted provider (ollama-local on
	// 127.0.0.1:11434) stays reachable from the dashboard on the same machine;
	// it is not a licence to dial the metadata service, which answers with
	// instance credentials. Checked before the bypass so there is one rule to
	// read rather than an exception inside an exception.
	if handlerutil.IsCloudMetadataURL(r.Context(), baseURL) {
		writeNodeValidation(w, map[string]any{"valid": false, "error": "URL not allowed"})
		return
	}

	// SSRF guard for remote callers; loopback peers keep self-hosted nodes
	// (e.g. ollama-local) reachable, mirroring upstream isLocalRequest.
	if !nodeRequestIsLocal(r) {
		if err := handlerutil.AssertPublicURL(baseURL); err != nil {
			writeNodeValidation(w, map[string]any{"valid": false, "error": "URL not allowed"})
			return
		}
	}

	ctx := r.Context()
	switch strings.TrimSpace(req.Type) {
	case "custom-embedding":
		validateCustomEmbeddingNode(w, ctx, baseURL, apiKey, strings.TrimSpace(req.ModelID))
	case "anthropic-compatible":
		validateAnthropicCompatibleNode(w, ctx, baseURL, apiKey, strings.TrimSpace(req.ModelID))
	default:
		validateOpenAICompatibleNode(w, ctx, baseURL, apiKey, strings.TrimSpace(req.ModelID))
	}
}

// writeNodeValidation writes a {valid, ...} validation outcome (HTTP 200 for
// every completed probe, matching the shape the dashboard Check button renders).
func writeNodeValidation(w http.ResponseWriter, payload map[string]any) {
	handlerutil.WriteJSON(w, http.StatusOK, payload)
}

// validateOpenAICompatibleNode probes GET {base}/models, falling back to a
// minimal chat request when the model endpoint is absent.
func validateOpenAICompatibleNode(w http.ResponseWriter, ctx context.Context, baseURL, apiKey, modelID string) {
	base := strings.TrimSuffix(baseURL, "/")
	headers := map[string]string{"Authorization": "Bearer " + apiKey}

	status, body, err := validateProbeDo(ctx, http.MethodGet, base+"/models", headers, nil)
	if err != nil {
		writeNodeValidation(w, map[string]any{"valid": false, "error": validateNodeNetworkMessage(err)})
		return
	}
	// Auth errors — no point trying the chat fallback.
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		writeNodeValidation(w, map[string]any{"valid": false, "error": "API key unauthorized"})
		return
	}

	// Verify via /chat/completions to avoid false-positive on public GET /models
	effectiveModel := modelID
	if effectiveModel == "" && len(body) > 0 {
		var modelsRes struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &modelsRes) == nil && len(modelsRes.Data) > 0 && modelsRes.Data[0].ID != "" {
			effectiveModel = modelsRes.Data[0].ID
		}
	}
	if effectiveModel != "" {
		payload, _ := json.Marshal(map[string]any{
			"model":      effectiveModel,
			"messages":   []map[string]string{{"role": "user", "content": "ping"}},
			"max_tokens": 1,
		})
		chatHeaders := map[string]string{
			"Authorization": "Bearer " + apiKey,
			"Content-Type":  "application/json",
		}
		chatStatus, _, cErr := validateProbeDo(ctx, http.MethodPost, base+"/chat/completions", chatHeaders, payload)
		if cErr == nil && (chatStatus == http.StatusUnauthorized || chatStatus == http.StatusForbidden) {
			writeNodeValidation(w, map[string]any{"valid": false, "error": "API key unauthorized"})
			return
		}
		if cErr != nil && status != http.StatusOK {
			writeNodeValidation(w, map[string]any{"valid": false, "error": validateNodeNetworkMessage(cErr)})
			return
		}
		if chatStatus == http.StatusOK {
			writeNodeValidation(w, map[string]any{"valid": true, "method": "chat"})
			return
		}
		if status != http.StatusOK {
			writeNodeValidation(w, map[string]any{"valid": false, "error": validateNodeChatStatusMessage(chatStatus), "method": "chat"})
			return
		}
	}

	if status == http.StatusOK {
		writeNodeValidation(w, map[string]any{"valid": true})
		return
	}

	writeNodeValidation(w, map[string]any{"valid": false, "error": validateNodeModelsStatusMessage(status)})
}

// validateAnthropicCompatibleNode probes GET {base}/models with x-api-key,
// falling back to a chat request when the model endpoint is absent.
func validateAnthropicCompatibleNode(w http.ResponseWriter, ctx context.Context, baseURL, apiKey, modelID string) {
	base := strings.TrimSpace(baseURL)
	if strings.HasSuffix(base, "/messages") {
		base = base[:len(base)-len("/messages")]
	}
	headers := map[string]string{
		"x-api-key":         apiKey,
		"anthropic-version": "2023-06-01",
		"Authorization":     "Bearer " + apiKey,
	}

	status, _, err := validateProbeDo(ctx, http.MethodGet, base+"/models", headers, nil)
	if err != nil {
		writeNodeValidation(w, map[string]any{"valid": false, "error": validateNodeNetworkMessage(err)})
		return
	}
	if status == http.StatusOK {
		writeNodeValidation(w, map[string]any{"valid": true})
		return
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		writeNodeValidation(w, map[string]any{"valid": false, "error": "API key unauthorized"})
		return
	}

	if modelID != "" {
		payload, _ := json.Marshal(map[string]any{
			"model":      modelID,
			"messages":   []map[string]string{{"role": "user", "content": "ping"}},
			"max_tokens": 1,
		})
		chatHeaders := map[string]string{
			"x-api-key":         apiKey,
			"anthropic-version": "2023-06-01",
			"Authorization":     "Bearer " + apiKey,
			"Content-Type":      "application/json",
		}
		chatStatus, _, err := validateProbeDo(ctx, http.MethodPost, base+"/chat/completions", chatHeaders, payload)
		if err != nil {
			writeNodeValidation(w, map[string]any{"valid": false, "error": validateNodeNetworkMessage(err)})
			return
		}
		if chatStatus == http.StatusOK {
			writeNodeValidation(w, map[string]any{"valid": true, "method": "chat"})
			return
		}
		writeNodeValidation(w, map[string]any{"valid": false, "error": validateNodeChatStatusMessage(chatStatus), "method": "chat"})
		return
	}

	writeNodeValidation(w, map[string]any{"valid": false, "error": validateNodeModelsStatusMessage(status)})
}

// validateCustomEmbeddingNode probes POST {base}/embeddings and reports the
// embedding dimension on success.
func validateCustomEmbeddingNode(w http.ResponseWriter, ctx context.Context, baseURL, apiKey, modelID string) {
	base := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if modelID == "" {
		writeNodeValidation(w, map[string]any{"valid": false, "error": "Model ID required for embedding validation"})
		return
	}
	payload, _ := json.Marshal(map[string]any{"model": modelID, "input": "ping"})
	headers := map[string]string{
		"Authorization": "Bearer " + apiKey,
		"Content-Type":  "application/json",
	}

	status, resBody, err := validateProbeDo(ctx, http.MethodPost, base+"/embeddings", headers, payload)
	if err != nil {
		writeNodeValidation(w, map[string]any{"valid": false, "error": validateNodeNetworkMessage(err)})
		return
	}
	if status == http.StatusOK {
		dims := embeddingDimension(resBody)
		payload := map[string]any{"valid": true, "method": "embeddings"}
		if dims > 0 {
			payload["dimensions"] = dims
		}
		writeNodeValidation(w, payload)
		return
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		writeNodeValidation(w, map[string]any{"valid": false, "error": "API key unauthorized"})
		return
	}
	msg := fmt.Sprintf("Embeddings request failed (%d)", status)
	if errText := strings.TrimSpace(string(resBody)); errText != "" {
		msg += ": " + errText
	}
	writeNodeValidation(w, map[string]any{"valid": false, "error": msg, "method": "embeddings"})
}

// embeddingDimension extracts the vector length of the first embedding from a
// successful /embeddings response, or 0 when the shape is unexpected.
func embeddingDimension(body []byte) int {
	var res struct {
		Data []struct {
			Embedding []any `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &res); err != nil || len(res.Data) == 0 {
		return 0
	}
	return len(res.Data[0].Embedding)
}

// isValidHTTPURL reports whether v is an absolute http(s) URL with a host.
func isValidHTTPURL(v string) bool {
	parsed, err := url.Parse(v)
	if err != nil || parsed.Host == "" {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

// loopbackRequestHosts are hosts considered local callers (upstream LOOPBACK_HOSTS).
var loopbackRequestHosts = map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}

// nodeRequestIsLocal mirrors dashboardGuard.isLocalRequest: loopback peers may
// reference self-hosted provider nodes, so the SSRF guard is skipped for them.
func nodeRequestIsLocal(r *http.Request) bool {
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = strings.Trim(h, "[]")
	}
	if loopbackRequestHosts[host] {
		return true
	}
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return loopbackRequestHosts[strings.Trim(strings.ToLower(remote), "[]")]
	}
	return false
}

// validateNodeNetworkMessage maps an outbound probe error to a user-friendly
// message, mirroring upstream route.js getErrorMessage.
func validateNodeNetworkMessage(err error) string {
	if err == nil {
		return ""
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "Request timeout - provider node not responding"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Request timeout - provider node not responding"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "DNS lookup failed - invalid domain or network issue"
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return "Connection refused - provider node offline or unreachable"
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return "SSL certificate verification failed"
	}
	return "Network connection failed - check URL and network connectivity"
}

// validateNodeModelsStatusMessage maps a /models probe status to a message.
func validateNodeModelsStatusMessage(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "API key unauthorized"
	case http.StatusNotFound:
		return "/models endpoint not found - try chat validation with model ID"
	default:
		if status >= 500 {
			return "Server error - try again later"
		}
		return fmt.Sprintf("Unexpected response (%d)", status)
	}
}

// validateNodeChatStatusMessage maps a /chat/completions probe status to a message.
func validateNodeChatStatusMessage(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "API key unauthorized"
	case http.StatusBadRequest:
		return "Invalid model or bad request"
	case http.StatusNotFound:
		return "Chat endpoint not found"
	default:
		if status >= 500 {
			return "Server error - try again later"
		}
		return fmt.Sprintf("Chat request failed (%d)", status)
	}
}
