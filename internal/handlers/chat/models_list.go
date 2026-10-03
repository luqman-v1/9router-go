package chat

import (
	"context"
	"regexp"
	"strings"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// ModelInfoObject represents a model entry in the /v1/models response. The
// key set mirrors upstream exactly: id, object, owned_by, capabilities and
// (for LLM entries) context_length / max_completion_tokens.
type ModelInfoObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
	// Capabilities is a per-model CapabilitiesDetail, or a ComboCapabilities
	// for combo entries — upstream publishes those two shapes with different
	// key sets, so the field cannot be one concrete type.
	Capabilities any `json:"capabilities,omitempty"`
	// ContextLength / MaxCompletionTokens are pointers because a combo can
	// only promise what every seat agrees on, and a combo with no LLM seat
	// promises nothing at all — it omits the keys rather than publish a zero
	// the client would read as a real limit. Upstream emits them on combo
	// entries too (comboSeatLimits in src/app/api/v1/models/route.js), taking
	// the smallest window in the combo's seat tree.
	ContextLength       *int `json:"context_length,omitempty"`
	MaxCompletionTokens *int `json:"max_completion_tokens,omitempty"`
}

// ConnectionHasCredential reports whether a provider connection carries auth
// material that can actually serve requests (apiKey, accessToken, authToken, or
// refreshToken; refreshToken alone is accepted because the token pipeline
// refreshes it on demand). Exported for dashboard consistency checks.
//
// NOTE: /v1/models listing itself does NOT use this — upstream
// src/app/api/v1/models/route.js filters connections on isActive only.
func ConnectionHasCredential(conn *models.ProviderConnection) bool {
	if conn == nil || conn.Data == "" {
		return false
	}
	var data struct {
		APIKey       string `json:"apiKey"`
		AccessToken  string `json:"accessToken"`
		AuthToken    string `json:"authToken"`
		RefreshToken string `json:"refreshToken"`
	}
	if err := json.Unmarshal([]byte(conn.Data), &data); err != nil {
		return false
	}
	return data.APIKey != "" || data.AccessToken != "" || data.AuthToken != "" || data.RefreshToken != ""
}

// connectionHasCredential is the package-local alias.
func connectionHasCredential(conn *models.ProviderConnection) bool {
	return ConnectionHasCredential(conn)
}

// Upstream kind hints for model ids with no per-model type metadata
// (src/app/api/v1/models/route.js inferKindFromUnknownModelId). The default
// /v1/models build uses kindFilter ["llm"], so anything these match is a
// media/embedding model and stays out of the LLM list.
var (
	embeddingIDHint = regexp.MustCompile(`(?i)embed`)
	ttsIDHint       = regexp.MustCompile(`(?i)tts|speech|audio|voice`)
	imageIDHint     = regexp.MustCompile(`(?i)image|imagen|dall-?e|flux|sdxl|sd-|stable-diffusion`)
)

// isLLMModelID mirrors upstream inferKindFromUnknownModelId.
func isLLMModelID(modelID string) bool {
	switch {
	case embeddingIDHint.MatchString(modelID):
		return false
	case ttsIDHint.MatchString(modelID):
		return false
	case imageIDHint.MatchString(modelID):
		return false
	default:
		return true
	}
}

// isLLMModelEntry resolves one merged id to its service kind the way upstream
// does: a custom row's own type wins (already filtered by isLLMCustomModel),
// then the registry entry's declared kind, and only ids with neither fall back
// to the id heuristic. The /v1/models list is the LLM list (kindFilter
// ["llm"]), so anything else stays out.
func isLLMModelEntry(outputAlias, staticAlias, providerID, modelID string, fromCustom bool) bool {
	if fromCustom {
		return true
	}
	for _, key := range []string{outputAlias, staticAlias, providerID} {
		if kind := providers.GetProviderModelKind(key, modelID); kind != "" {
			return isLLMServiceKind(kind)
		}
	}
	return isLLMModelID(modelID)
}

// nonLLMServiceKinds mirrors upstream MODEL_TYPE_TO_KIND: only these six kinds
// leave the llm list. A kind outside this set (e.g. the `systemone` marker
// some registry entries carry) is treated as an LLM, exactly like
// modelKind() → `MODEL_TYPE_TO_KIND[k] || LLM_KIND`.
var nonLLMServiceKinds = map[string]bool{
	"image":       true,
	"tts":         true,
	"embedding":   true,
	"stt":         true,
	"imagetotext": true,
	"video":       true,
}

func isLLMServiceKind(kind string) bool {
	return !nonLLMServiceKinds[strings.ToLower(strings.TrimSpace(kind))]
}

// isLLMCustomModel mirrors upstream modelKind() for a kv.customModels row: the
// row's own `type` decides, and — per MODEL_TYPE_TO_KIND — only image, tts,
// embedding, stt, imageToText and video leave the LLM list. Anything else
// (including unknown types like "systemone") is an LLM.
func isLLMCustomModel(modelType string) bool {
	return isLLMServiceKind(modelType)
}

// connectionModelData is the subset of the connection data blob upstream reads
// (providerSpecificData.prefix / providerSpecificData.enabledModels, plus the
// top-level variants older dashboards wrote).
type connectionModelData struct {
	Prefix               string   `json:"prefix"`
	EnabledModelsTop     []string `json:"enabledModels"`
	ProviderSpecificData struct {
		Prefix        string   `json:"prefix"`
		EnabledModels []string `json:"enabledModels"`
	} `json:"providerSpecificData"`
}

func parseConnectionModelData(conn *models.ProviderConnection) connectionModelData {
	var data connectionModelData
	if conn != nil && conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &data)
	}
	return data
}

// EnabledModels returns the model ids the operator pinned on the connection,
// preferring providerSpecificData.enabledModels over the top-level field the
// same way buildModelsList does.
func (c connectionModelData) EnabledModels() []string {
	if ids := c.ProviderSpecificData.EnabledModels; len(ids) > 0 {
		return ids
	}
	return c.EnabledModelsTop
}

// stripModelPrefix removes a leading "<prefix>/" qualifier the way upstream
// strips outputAlias/, staticAlias/ and providerId/ from rawModelIds.
func stripModelPrefix(modelID string, prefixes ...string) string {
	for _, p := range prefixes {
		if p == "" {
			continue
		}
		if strings.HasPrefix(modelID, p+"/") {
			return modelID[len(p)+1:]
		}
	}
	return modelID
}

func isCompatibleProviderID(providerID string) bool {
	return strings.HasPrefix(providerID, "openai-compatible-") ||
		strings.HasPrefix(providerID, "anthropic-compatible-")
}

// disabledModelIndex collects the `disabledModels` KV scope the dashboard
// writes to, keyed by both the stored alias and its canonical provider.
func (h *ChatHandler) disabledModelIndex() map[string]map[string]bool {
	disabled := make(map[string]map[string]bool)
	if h.Repo == nil {
		return disabled
	}
	disabledKV, err := h.Repo.GetKVScope("disabledModels")
	if err != nil {
		return disabled
	}
	for prov, raw := range disabledKV {
		var ids []string
		if perr := json.Unmarshal([]byte(raw), &ids); perr != nil {
			continue
		}
		set := make(map[string]bool, len(ids))
		for _, id := range ids {
			if id = strings.TrimSpace(id); id != "" {
				set[id] = true
			}
		}
		if len(set) == 0 {
			continue
		}
		disabled[prov] = set
		if canon := providers.ResolveAlias(prov); canon != "" {
			if _, ok := disabled[canon]; !ok {
				disabled[canon] = set
			}
		}
	}
	return disabled
}

// ModelsListMode selects which provider set buildModelsList walks. The zero
// value is modeListAll, so every existing caller keeps the upstream-faithful
// behaviour: static catalog dump when no connection rows exist, otherwise one
// model set per active connection.
type ModelsListMode int

const (
	// modeListAll is the upstream default: catalog dump on a fresh install,
	// connection-scoped otherwise.
	modeListAll ModelsListMode = iota
	// modeListConnected restricts output to providers that have an active
	// connection, plus registry noAuth providers (usable without credentials).
	// With no connection rows at all this yields the noAuth subset instead of
	// the full 1300-entry catalog, which is what a client asking for "what can
	// I actually call" wants.
	modeListConnected
	// modeListCatalog forces the full static catalog dump plus custom models
	// even when connections exist, for explicit discovery requests.
	modeListCatalog
)

// ModelsListResult carries the model list plus the metadata the handler echoes
// back, so a caller can tell a candidate catalog from a usable one.
type ModelsListResult struct {
	Models []ModelInfoObject
	Mode   string
	// Connections is the number of active provider connections considered.
	Connections int
}

// modelsListModeString maps a mode to the wire value published as "mode".
func modelsListModeString(mode ModelsListMode) string {
	switch mode {
	case modeListConnected:
		return "connected"
	case modeListCatalog:
		return "catalog"
	default:
		return "all"
	}
}

// connectedProviderIDs is the set of provider ids with at least one active
// connection, plus every noAuth registry provider (usable with no credential).
func (h *ChatHandler) connectedProviderIDs(conns []*models.ProviderConnection) map[string]bool {
	ids := make(map[string]bool, len(conns))
	for _, conn := range conns {
		if conn == nil || conn.Provider == "" || conn.IsActive == 0 {
			continue
		}
		ids[conn.Provider] = true
		if canon := providers.ResolveAlias(conn.Provider); canon != "" {
			ids[canon] = true
		}
	}
	for id, cfg := range providers.KnownProviders {
		if cfg.NoAuth {
			ids[id] = true
		}
	}
	return ids
}

// isUsableProvider reports whether a provider id (or its canonical form) is
// reachable: either connected, or noAuth in the registry.
func isUsableProvider(providerID string, usable map[string]bool) bool {
	if providerID == "" {
		return false
	}
	if usable[providerID] {
		return true
	}
	canon := providers.ResolveAlias(providerID)
	return canon != "" && usable[canon]
}

// buildModelsListResult mirrors upstream buildModelsList(["llm"]) and reports
// the mode it used. Combos come first, then one model set per active
// connection, with the static catalog dump only when the connections table
// itself is empty (and unconditionally in modeListCatalog).
func (h *ChatHandler) buildModelsListResult(ctx context.Context, mode ModelsListMode) ModelsListResult {
	var allConns []*models.ProviderConnection
	if h.Repo != nil {
		allConns, _ = h.Repo.GetProviderConnections("", false)
	}

	// Connections is the number of distinct providers with an active
	// connection, counted from the connection rows directly: `usable` also
	// folds in noAuth providers and canonical aliases, which would
	// double-count a single connection.
	activeCount := 0
	seenProviders := make(map[string]bool, len(allConns))
	for _, conn := range allConns {
		if conn == nil || conn.Provider == "" || conn.IsActive == 0 {
			continue
		}
		canon := providers.ResolveAlias(conn.Provider)
		if seenProviders[canon] {
			continue
		}
		seenProviders[canon] = true
		activeCount++
	}
	activeConns := activeConnections(allConns)
	usable := h.connectedProviderIDs(allConns)

	data := h.buildModelsListForMode(ctx, mode, allConns, activeConns, usable)
	return ModelsListResult{Models: data, Mode: modelsListModeString(mode), Connections: activeCount}
}

// activeConnections filters the connection rows down to the ones the operator
// left enabled. It exists so the catalog gate in buildModelsListForMode and the
// reported Connections count agree on what "configured" means: a row the
// dashboard disabled is still a row, and counting it made disabling every
// connection answer /v1/models with an empty list (#46).
func activeConnections(conns []*models.ProviderConnection) []*models.ProviderConnection {
	active := make([]*models.ProviderConnection, 0, len(conns))
	for _, conn := range conns {
		if conn == nil || conn.Provider == "" || conn.IsActive == 0 {
			continue
		}
		active = append(active, conn)
	}
	return active
}

// buildModelsList is the upstream-faithful entry point used by model lookup.
func (h *ChatHandler) buildModelsList(ctx context.Context) []ModelInfoObject {
	conns := h.allConnections()
	return h.buildModelsListForMode(ctx, modeListAll, conns, activeConnections(conns), nil)
}

// allConnections returns every provider connection row, or nil without a Repo.
func (h *ChatHandler) allConnections() []*models.ProviderConnection {
	if h.Repo == nil {
		return nil
	}
	conns, _ := h.Repo.GetProviderConnections("", false)
	return conns
}

// buildModelsListForMode is the shared builder. usable may be nil for
// modeListAll, in which case no provider filtering is applied.
func (h *ChatHandler) buildModelsListForMode(
	ctx context.Context,
	mode ModelsListMode,
	allConns []*models.ProviderConnection,
	activeConns []*models.ProviderConnection,
	usable map[string]bool,
) []ModelInfoObject {
	var data []ModelInfoObject
	seen := make(map[string]bool)
	disabled := h.disabledModelIndex()
	isDisabled := func(provider, modelID string) bool {
		return disabled[provider][modelID]
	}
	filterConnected := mode == modeListConnected && usable != nil

	// 1. Combos first (upstream pushes them before provider models).
	data = h.appendCombos(data, seen)

	// 2. Nothing to scope to: static catalog dump + custom models, so a fresh
	// install still has a usable picker (upstream connections.length === 0).
	// In connected mode the dump is narrowed to providers that are actually
	// usable, so the endpoint stops advertising the whole catalog.
	//
	// Rows exist but none of them is active — every connection disabled in the
	// dashboard — is the same situation, and gating on row count answered it
	// with an empty list (#46). It is treated as "no usable credentialed
	// provider": the answer narrows to the noAuth subset rather than the whole
	// catalog, because unlike a genuinely fresh install there IS a configured
	// install here and dumping 1300+ models would re-advertise providers the
	// operator explicitly switched off. A provider whose rows are ALL inactive
	// still publishes nothing, since filterConnected keeps it out of the dump.
	catalogWhenEmpty := len(activeConns) == 0
	if catalogWhenEmpty && len(allConns) > 0 && !filterConnected && mode != modeListCatalog {
		filterConnected = true
	}

	if catalogWhenEmpty || mode == modeListCatalog {
		for alias, models := range providers.ProviderModels {
			if canon := providers.ResolveAlias(alias); canon != alias && providers.GetProviderAlias(canon) != alias {
				continue
			}
			if filterConnected && !isUsableProvider(alias, usable) {
				continue
			}
			for _, mID := range models {
				if isDisabled(alias, mID) || !isLLMModelEntry(alias, alias, alias, mID, false) {
					continue
				}
				data = appendStaticModel(data, seen, alias, mID)
			}
		}
		data = h.appendLooseCustomModels(data, seen, disabled, filterConnected, usable)
		return finalizeModels(data)
	}

	// 3. One connection per provider, isActive !== false (upstream filters on
	// isActive only — no credential requirement for listing).
	firstPerProvider := make(map[string]*models.ProviderConnection)
	order := make([]string, 0, len(allConns))
	for _, conn := range allConns {
		if conn.Provider == "" || conn.IsActive == 0 {
			continue
		}
		if filterConnected && !isUsableProvider(conn.Provider, usable) {
			continue
		}
		if _, ok := firstPerProvider[conn.Provider]; !ok {
			firstPerProvider[conn.Provider] = conn
			order = append(order, conn.Provider)
		}
	}

	customs := h.customModelsByProvider()
	aliases := h.modelAliasTargets()

	for _, provID := range order {
		conn := firstPerProvider[provID]
		data = h.appendConnectionModels(ctx, data, seen, conn, provID, customs, aliases, isDisabled)
	}
	// 4. Connected mode additionally publishes the noAuth registry providers
	// that have no connection row, mirroring the dashboard picker, which keeps
	// every noAuth provider selectable regardless of connections. A provider
	// already covered by an active connection is skipped: that connection's
	// enabledModels / live catalog has decided its model set, and re-adding the
	// static catalog here would re-publish models the connection pinned out.
	if filterConnected {
		data = appendNoAuthCatalogModels(data, seen, order, isDisabled)
	}

	return finalizeModels(data)
}

// appendConnectionModels reproduces the per-connection branch of upstream
// buildModelsList: enabledModels override, else the static catalog, merged with
// custom models and alias targets registered for that same provider.
func (h *ChatHandler) appendConnectionModels(
	ctx context.Context,
	data []ModelInfoObject,
	seen map[string]bool,
	conn *models.ProviderConnection,
	providerID string,
	customs map[string][]*db.CustomModel,
	aliases map[string]string,
	isDisabled func(provider, modelID string) bool,
) []ModelInfoObject {
	connData := parseConnectionModelData(conn)

	staticAlias := providers.GetProviderAlias(providerID)
	if staticAlias == "" {
		staticAlias = providerID
	}
	outputAlias := staticAlias
	switch {
	case connData.ProviderSpecificData.Prefix != "":
		outputAlias = connData.ProviderSpecificData.Prefix
	case connData.Prefix != "":
		outputAlias = connData.Prefix
	}

	ids := connData.EnabledModels()
	// Upstream: a live catalog replaces the static one only when the operator
	// has not pinned enabledModels on the connection.
	liveByID := make(map[string]LiveModel)
	if len(ids) == 0 {
		var sharedData shared.ConnectionData
		if conn.Data != "" {
			_ = json.Unmarshal([]byte(conn.Data), &sharedData)
		}
		for _, live := range h.resolveLiveCatalog(ctx, conn, &sharedData, providerID) {
			liveByID[live.ID] = live
			ids = append(ids, live.ID)
		}
	}
	if len(ids) == 0 && !isCompatibleProviderID(providerID) {
		ids = providers.GetProviderModels(staticAlias)
		if len(ids) == 0 {
			ids = providers.GetProviderModels(outputAlias)
		}
	}

	merged := make([]string, 0, len(ids))
	seenID := make(map[string]bool, len(ids))
	typedCustom := make(map[string]bool, len(ids))
	typedCustomLimits := make(map[string][2]int)
	// Upstream strips the outputAlias/staticAlias/providerId qualifier only from
	// registry + enabledModels ids and from alias targets; a custom row's id is
	// used verbatim — that is why `openrouter/openrouter/free` stays
	// double-prefixed upstream.
	add := func(raw string, fromCustom bool) {
		modelID := strings.TrimSpace(raw)
		if !fromCustom {
			modelID = stripModelPrefix(modelID, outputAlias, staticAlias, providerID)
		}
		if modelID == "" || seenID[modelID] {
			return
		}
		seenID[modelID] = true
		typedCustom[modelID] = fromCustom
		merged = append(merged, modelID)
	}
	for _, raw := range ids {
		add(raw, false)
	}
	// Custom models registered for this provider. Upstream matches
	// alias === staticAlias || outputAlias || providerId.
	for _, key := range []string{staticAlias, outputAlias, providerID} {
		for _, cm := range customs[key] {
			if !isLLMCustomModel(cm.Type) {
				continue
			}
			// Upstream resolves custom-model capabilities from the saved caps,
			// so publish them before the capability lookup below.
			h.registerCustomModelCaps(outputAlias, key, cm)
			cmCtxLen, cmMaxOut := declaredTokenLimits(cm)
			typedCustomLimits[cm.ID] = [2]int{cmCtxLen, cmMaxOut}
			add(cm.ID, true)
		}
	}
	// Legacy alias targets pointing at this provider.
	for _, target := range aliases {
		if strings.HasPrefix(target, outputAlias+"/") ||
			strings.HasPrefix(target, staticAlias+"/") ||
			strings.HasPrefix(target, providerID+"/") {
			add(target, false)
		}
	}

	for _, modelID := range merged {
		if isDisabled(outputAlias, modelID) || isDisabled(staticAlias, modelID) {
			continue
		}
		// Upstream resolves kind from the custom row's own type, then the
		// registry entry's declared kind, and only then falls back to the id
		// heuristic (modelKind → staticModelKindById → inferKindFromUnknownModelId).
		if !isLLMModelEntry(outputAlias, staticAlias, providerID, modelID, typedCustom[modelID]) {
			continue
		}
		fullID := outputAlias + "/" + modelID
		if seen[fullID] {
			continue
		}
		seen[fullID] = true

		declaredCtxLen, declaredMaxOut := typedCustomLimits[modelID][0], typedCustomLimits[modelID][1]

		ctxLen, maxOut := declaredCtxLen, declaredMaxOut
		if ctxLen == 0 || maxOut == 0 {
			tableCtxLen, tableMaxOut := providers.GetModelTokenLimits(modelID)
			if tableCtxLen == 0 && tableMaxOut == 0 {
				tableCtxLen, tableMaxOut = providers.GetModelTokenLimits(fullID)
			}
			if ctxLen == 0 {
				ctxLen = tableCtxLen
			}
			if maxOut == 0 {
				maxOut = tableMaxOut
			}
		}
		caps := providers.GetCapabilitiesDetailForModel(providerID, modelID)
		if caps.ContextWindow > 0 && ctxLen == 0 {
			ctxLen = caps.ContextWindow
		}
		if maxOut == 0 {
			maxOut = caps.MaxOutput
		}
		// A live resolver may publish its own capability block (kiro does:
		// {thinking, agentic}). Upstream then uses that block verbatim and
		// fills the window from the static fallback, so a kiro model reports
		// context_length from the table default rather than the live payload.
		if live, ok := liveByID[modelID]; ok && live.Capabilities != nil {
			data = append(data, ModelInfoObject{
				ID:                  fullID,
				Object:              "model",
				OwnedBy:             outputAlias,
				Capabilities:        live.Capabilities,
				ContextLength:       optionalTokenLimit(ctxLen),
				MaxCompletionTokens: optionalTokenLimit(maxOut),
			})
			continue
		}
		// Live catalogs (grok-cli) publish limits the static table may not
		// know; use them when it has nothing to say.
		if live, ok := liveByID[modelID]; ok {
			if ctxLen == 0 {
				ctxLen = live.ContextLength
			}
			if maxOut == 0 {
				maxOut = live.MaxOutput
			}
		}
		data = append(data, ModelInfoObject{
			ID:                  fullID,
			Object:              "model",
			OwnedBy:             outputAlias,
			Capabilities:        &caps,
			ContextLength:       optionalTokenLimit(ctxLen),
			MaxCompletionTokens: optionalTokenLimit(maxOut),
		})
	}
	return data
}

// appendNoAuthCatalogModels publishes the static catalog of every noAuth
// registry provider that has no active connection row. Connected mode lists
// connections only, so without this pass a single configured connection would
// make every noAuth provider (opencode and friends) vanish from the response
// while the dashboard picker still shows them. Providers in `connected` are
// skipped because their connection already decided the model set.
func appendNoAuthCatalogModels(
	data []ModelInfoObject,
	seen map[string]bool,
	connected []string,
	isDisabled func(provider, modelID string) bool,
) []ModelInfoObject {
	covered := make(map[string]bool, len(connected)*2)
	for _, provID := range connected {
		covered[provID] = true
		if canon := providers.ResolveAlias(provID); canon != "" {
			covered[canon] = true
		}
		if alias := providers.GetProviderAlias(provID); alias != "" {
			covered[alias] = true
		}
	}

	for alias, modelIDs := range providers.ProviderModels {
		if covered[alias] {
			continue
		}
		// Publish a provider once, under the alias upstream uses.
		if canon := providers.ResolveAlias(alias); canon != alias && providers.GetProviderAlias(canon) != alias {
			continue
		}
		if !providers.IsNoAuthProvider(alias) {
			continue
		}
		for _, modelID := range modelIDs {
			if isDisabled(alias, modelID) || !isLLMModelEntry(alias, alias, alias, modelID, false) {
				continue
			}
			data = appendStaticModel(data, seen, alias, modelID)
		}
	}
	return data
}

// appendStaticModel emits one static-registry entry (fresh-install path).
func appendStaticModel(data []ModelInfoObject, seen map[string]bool, alias, modelID string) []ModelInfoObject {
	fullID := alias + "/" + modelID
	if seen[fullID] {
		return data
	}
	seen[fullID] = true

	ctxLen, maxOut := providers.GetModelTokenLimits(modelID)
	caps := providers.GetCapabilitiesDetailForModel(alias, modelID)
	if caps.ContextWindow > 0 && ctxLen == 0 {
		ctxLen = caps.ContextWindow
	}
	if maxOut == 0 {
		maxOut = caps.MaxOutput
	}
	return append(data, ModelInfoObject{
		ID:                  fullID,
		Object:              "model",
		OwnedBy:             alias,
		Capabilities:        &caps,
		ContextLength:       optionalTokenLimit(ctxLen),
		MaxCompletionTokens: optionalTokenLimit(maxOut),
	})
}

// appendLooseCustomModels lists custom models on a fresh install (upstream
// lists every llm-typed custom row when there are no connections at all).
func (h *ChatHandler) appendLooseCustomModels(
	data []ModelInfoObject,
	seen map[string]bool,
	disabled map[string]map[string]bool,
	filterConnected bool,
	usable map[string]bool,
) []ModelInfoObject {
	prefixMap := h.providerNodePrefixMap()
	customs := h.customModelsByProvider()
	for providerID, list := range customs {
		if filterConnected && !isUsableProvider(providerID, usable) {
			continue
		}
		prefix := providerID
		if mapped, ok := prefixMap[providerID]; ok && mapped != "" {
			prefix = mapped
		}
		for _, cm := range list {
			if !isLLMCustomModel(cm.Type) {
				continue
			}
			if disabled[providerID][cm.ID] || disabled[prefix][cm.ID] {
				continue
			}
			h.registerCustomModelCaps(prefix, providerID, cm)
			// Upstream filters custom rows on their own `type` only — the id
			// heuristic is never applied to a custom model.
			fullID := prefix + "/" + cm.ID
			if seen[fullID] {
				continue
			}
			seen[fullID] = true
			ctxLen, maxOut := declaredTokenLimits(cm)
			if ctxLen == 0 || maxOut == 0 {
				tableCtxLen, tableMaxOut := providers.GetModelTokenLimits(fullID)
				if tableCtxLen == 0 && tableMaxOut == 0 {
					tableCtxLen, tableMaxOut = providers.GetModelTokenLimits(cm.ID)
				}
				if ctxLen == 0 {
					ctxLen = tableCtxLen
				}
				if maxOut == 0 {
					maxOut = tableMaxOut
				}
			}
			caps := providers.GetCapabilitiesDetailForModel(prefix, cm.ID)
			if caps.ContextWindow > 0 && ctxLen == 0 {
				ctxLen = caps.ContextWindow
			}
			if maxOut == 0 {
				maxOut = caps.MaxOutput
			}
			data = append(data, ModelInfoObject{
				ID:                  fullID,
				Object:              "model",
				OwnedBy:             prefix,
				Capabilities:        &caps,
				ContextLength:       optionalTokenLimit(ctxLen),
				MaxCompletionTokens: optionalTokenLimit(maxOut),
			})
		}
	}
	return data
}

// registerCustomModelCaps publishes the capability flags and token limits
// saved on a custom model so capability lookups elsewhere see them, like
// upstream customModelCaps.
func (h *ChatHandler) registerCustomModelCaps(prefix, providerID string, cm *db.CustomModel) {
	ctxLen, maxOut := declaredTokenLimits(cm)
	if len(cm.Caps) == 0 && ctxLen == 0 && maxOut == 0 {
		return
	}
	var caps providers.Capabilities
	if cm.Caps["vision"] {
		caps.Vision = true
	}
	if cm.Caps["reasoning"] {
		caps.Reasoning = true
	}
	if cm.Caps["search"] {
		caps.Search = true
	}
	if cm.Caps["tools"] {
		caps.Tools = true
	}
	if cm.Caps["image"] || cm.Caps["imageOutput"] {
		caps.ImageOutput = true
	}
	if cm.Caps["audio"] {
		caps.AudioInput = true
	}
	caps.ContextWindow = ctxLen
	caps.MaxOutput = maxOut
	providers.SetCustomModelCaps(prefix, cm.ID, caps)
	if prefix != providerID {
		providers.SetCustomModelCaps(providerID, cm.ID, caps)
	}
}

// declaredTokenLimits reads the limits the operator set on a custom model
// row. Zero means "not declared" and hands the decision back to the substring
// table, so a row saved before these fields existed keeps behaving exactly as
// it did.
func declaredTokenLimits(cm *db.CustomModel) (contextWindow int, maxOutput int) {
	if cm == nil {
		return 0, 0
	}
	return cm.ContextWindow, cm.MaxOutput
}

// declaredTokenLimitsFor returns the limits a custom model row declared for
// one provider/model pair. The row is addressed by the alias the client sees
// or by the provider node's storage id, since a custom row is keyed by the
// latter while /v1/models publishes it under the former. Returns (0, 0) when
// nothing was declared, which hands the decision back to the substring table.
func (h *ChatHandler) declaredTokenLimitsFor(provider, modelID string) (contextWindow int, maxOutput int) {
	if h.Repo == nil || modelID == "" || provider == "" {
		return 0, 0
	}
	bare := modelID
	if _, after, ok := strings.CutLast(modelID, "/"); ok {
		bare = after
	}
	customs := h.customModelsByProvider()
	keys := []string{provider}
	if node, _, err := h.Repo.GetProviderNodeByPrefix(provider); err == nil && node != nil {
		keys = append(keys, node.ID)
	}
	for _, key := range keys {
		for _, cm := range customs[key] {
			if cm.ID == bare {
				return declaredTokenLimits(cm)
			}
		}
	}
	return 0, 0
}

// optionalTokenLimit boxes a token limit for ModelInfoObject: a non-positive
// value is "nothing known", which the entry omits rather than publishes.
func optionalTokenLimit(limit int) *int {
	if limit <= 0 {
		return nil
	}
	return &limit
}

// comboIndex is the per-request combo catalogue shared by appendCombos and the
// seat walk. Reading one combo used to be a DB round trip, so a single combo
// listing issued several of them (one per seat, plus a settings read inside
// each) and a cyclic definition turned that into an unbounded query loop.
type comboIndex map[string][]string

// loadComboIndex reads every combo's seat list once. Rows whose models column
// is not a JSON array contribute no entry: they carry no seats to publish.
func loadComboIndex(combos []*models.Combo) comboIndex {
	index := make(comboIndex, len(combos))
	for _, combo := range combos {
		if combo == nil || combo.Name == "" {
			continue
		}
		var seats []string
		if json.Unmarshal([]byte(combo.Models), &seats) != nil {
			continue
		}
		index[combo.Name] = seats
	}
	return index
}

// webSeatSuffixes are the seat suffixes the dashboard uses for the webSearch /
// webFetch entries it publishes on a provider (web/src/lib/mediaTypes.ts and
// upstream src/app/api/v1/models/route.js, which appends `<alias>/search` and
// `<alias>/fetch`). Such a seat is a web tool, not a chat model: it carries no
// window of its own, and letting one vote on the minimum would publish the
// capability floor as if the whole combo had it.
var webSeatSuffixes = [...]string{"/search", "/fetch"}

// isNonLLMComboSeat reports whether a seat names a web search / fetch endpoint
// rather than a chat model. Mirrors the non-LLM filter /v1/models already
// applies per provider entry (isLLMModelEntry), which upstream's comboSeatLimits
// has no equivalent of and which therefore folds the floor into a web seat.
func isNonLLMComboSeat(seat string) bool {
	slash := strings.IndexByte(seat, '/')
	if slash <= 0 {
		return false
	}
	for _, suffix := range webSeatSuffixes {
		if strings.EqualFold(seat[slash:], suffix) {
			return true
		}
	}
	return false
}

// comboSeatCapabilities resolves one concrete "provider/model" seat to its
// capability block, mirroring upstream comboSeatCapabilities: the seat's
// prefix is a UI alias, and the tables are keyed by provider id, so the alias
// is mapped first.
func comboSeatCapabilities(seat string) providers.CapabilitiesDetail {
	providerID, modelID := "", seat
	if slash := strings.IndexByte(seat, '/'); slash > 0 {
		prefix := seat[:slash]
		modelID = seat[slash+1:]
		providerID = providers.ResolveAlias(prefix)
	}
	return providers.GetCapabilitiesDetailForModel(providerID, modelID)
}

// comboSeatLimits walks a combo's whole seat tree and returns the smallest
// context window and the smallest max output any seat in it can serve. A combo
// can hand the request to any seat, so the only limit it can promise is its
// narrowest one; upstream takes the same minimum (Math.min over seats), while
// aggregateComboCapabilities keeps the widest maxOutput for its capabilities
// block.
//
// A seat that is itself a combo name contributes its own leaves' minimum, and
// cycles are cut: a name already on the walk contributes nothing, which is
// what bounds a self-referencing combo (upstream's `visiting` set).
func comboSeatLimits(index comboIndex, comboName string) (contextWindow, maxOutput int) {
	visiting := make(map[string]bool, 4)
	visiting[comboName] = true
	return minComboSeatLimits(index, index[comboName], visiting)
}

func minComboSeatLimits(index comboIndex, seats []string, visiting map[string]bool) (contextWindow, maxOutput int) {
	for _, seat := range seats {
		if !strings.Contains(seat, "/") {
			nested, ok := index[seat]
			if !ok || visiting[seat] {
				continue
			}
			visiting[seat] = true
			nestedWindow, nestedMax := minComboSeatLimits(index, nested, visiting)
			delete(visiting, seat)
			contextWindow, maxOutput = foldTokenMinimum(contextWindow, maxOutput, nestedWindow, nestedMax)
			continue
		}
		if isNonLLMComboSeat(seat) {
			continue
		}
		caps := comboSeatCapabilities(seat)
		contextWindow, maxOutput = foldTokenMinimum(contextWindow, maxOutput, caps.ContextWindow, caps.MaxOutput)
	}
	return contextWindow, maxOutput
}

// foldTokenMinimum keeps the smallest positive value seen for each limit. Zero
// means "unknown", so it never becomes the minimum and never suppresses a
// value another seat does know.
func foldTokenMinimum(currentWindow, currentMax, candidateWindow, candidateMax int) (int, int) {
	if candidateWindow > 0 && (currentWindow == 0 || candidateWindow < currentWindow) {
		currentWindow = candidateWindow
	}
	if candidateMax > 0 && (currentMax == 0 || candidateMax < currentMax) {
		currentMax = candidateMax
	}
	return currentWindow, currentMax
}

// appendCombos lists combo names with the union of their leaf capabilities,
// matching upstream aggregateComboCapabilities, plus the combo-wide token
// limits upstream comboSeatLimits derives.
func (h *ChatHandler) appendCombos(data []ModelInfoObject, seen map[string]bool) []ModelInfoObject {
	if h.Repo == nil {
		return data
	}
	combos, err := h.Repo.GetCombos()
	if err != nil {
		return data
	}
	index := loadComboIndex(combos)
	for _, combo := range combos {
		if combo == nil || combo.Name == "" || seen[combo.Name] {
			continue
		}
		// Upstream comboMatchesKinds: this endpoint is the LLM list
		// (kindFilter ["llm"]), so webSearch/webFetch combos are excluded —
		// they only appear under /v1/models/web. No kind (or an explicit
		// "llm" kind) means an LLM combo.
		if combo.Kind != nil {
			if kind := strings.TrimSpace(*combo.Kind); kind != "" && kind != "llm" {
				continue
			}
		}
		seen[combo.Name] = true

		entry := ModelInfoObject{
			ID:      combo.Name,
			Object:  "model",
			OwnedBy: "combo",
		}
		if caps, ok := h.aggregateComboCapabilities(combo.Name); ok {
			entry.Capabilities = caps
		}
		// Upstream publishes the combo-wide limits at the top level too (any
		// seat can serve the request, so the window is the smallest one in the
		// tree). A combo with no LLM seat at all publishes neither key rather
		// than a guess.
		contextWindow, maxOutput := comboSeatLimits(index, combo.Name)
		entry.ContextLength = optionalTokenLimit(contextWindow)
		entry.MaxCompletionTokens = optionalTokenLimit(maxOutput)
		data = append(data, entry)
	}
	return data
}

// aggregateComboCapabilities folds the leaf capabilities with upstream's
// aggregateComboCapabilities rules (`some` for most booleans, `every` for
// tools, first-leaf thinking fields, narrowest contextWindow, widest
// maxOutput) and returns the combo-specific shape.
func (h *ChatHandler) aggregateComboCapabilities(comboName string) (*providers.ComboCapabilities, bool) {
	combo, err := h.Repo.GetComboByName(comboName)
	if err != nil || combo == nil || combo.Models == "" {
		return nil, false
	}
	var leaves []string
	if err := json.Unmarshal([]byte(combo.Models), &leaves); err != nil || len(leaves) == 0 {
		return nil, false
	}
	flattened, flatErr := h.flattenComboModels(leaves)
	if flatErr != nil {
		flattened = leaves
	}

	caps := make([]providers.CapabilitiesDetail, 0, len(flattened))
	for _, leaf := range flattened {
		// Upstream aggregateComboCapabilities takes the provider's per-model
		// capability block verbatim. A live catalog resolver may publish a
		// whole block instead (kiro does: {thinking, agentic}), and then the
		// static table is no longer what describes this model, so its limits
		// must not be folded in as if they were known.
		if live, ok := h.liveCapabilityLeaf(leaf); ok {
			caps = append(caps, providers.CapabilitiesDetail{
				ThinkingCanDisable: true,
				Reasoning:          live.Capabilities.Thinking,
				ContextWindow:      live.ContextLength,
				MaxOutput:          live.MaxOutput,
			})
			continue
		}
		providerID, modelID := "", leaf
		if info := h.resolveModelEntry(leaf); info != nil {
			providerID, modelID = info.Provider, info.Model
		}
		caps = append(caps, providers.GetCapabilitiesDetailForModel(providerID, modelID))
	}
	merged, ok := providers.AggregateComboCapabilities(caps)
	if !ok {
		return nil, false
	}
	return &merged, true
}

// liveCapabilityLeaf returns the live capability block describing a combo
// seat, when a live catalog resolver publishes one for it. The connection for
// the seat's provider is resolved first because the live catalogs are
// per-connection (kiro: whoami + model list, grok-cli: /models).
func (h *ChatHandler) liveCapabilityLeaf(seat string) (LiveModel, bool) {
	if h.Repo == nil {
		return LiveModel{}, false
	}
	info := h.resolveModelEntry(seat)
	if info == nil {
		return LiveModel{}, false
	}
	conns, err := h.Repo.GetProviderConnections(info.Provider, true)
	if err != nil || len(conns) == 0 {
		return LiveModel{}, false
	}
	conn := conns[0]
	var connData shared.ConnectionData
	if conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &connData)
	}
	for _, live := range h.resolveLiveCatalog(context.Background(), conn, &connData, info.Provider) {
		if live.Capabilities != nil && live.ID == info.Model {
			return live, true
		}
	}
	return LiveModel{}, false
}

func finalizeModels(data []ModelInfoObject) []ModelInfoObject {
	if data == nil {
		return []ModelInfoObject{}
	}
	return data
}

// providerNodePrefixMap maps providerNode.id → its registered display prefix.
func (h *ChatHandler) providerNodePrefixMap() map[string]string {
	if h.Repo == nil {
		return nil
	}
	prefixMap, err := h.Repo.GetProviderNodePrefixMap()
	if err != nil {
		return nil
	}
	return prefixMap
}

// customModelsByProvider indexes kv.customModels rows by their stored
// providerAlias so a connection can pick up the models registered for it.
func (h *ChatHandler) customModelsByProvider() map[string][]*db.CustomModel {
	out := make(map[string][]*db.CustomModel)
	if h.Repo == nil {
		return out
	}
	customs, err := h.Repo.GetCustomModels()
	if err != nil {
		return out
	}
	for _, cm := range customs {
		if cm == nil || cm.ProviderAlias == "" || cm.ID == "" {
			continue
		}
		out[cm.ProviderAlias] = append(out[cm.ProviderAlias], cm)
	}
	return out
}

// modelAliasTargets returns the alias name → target model mapping. Upstream
// merges those targets into the owning provider's list instead of publishing
// the alias key as its own model.
func (h *ChatHandler) modelAliasTargets() map[string]string {
	out := make(map[string]string)
	if h.Repo == nil {
		return out
	}
	aliases, err := h.Repo.GetModelAliases()
	if err != nil {
		return out
	}
	for name, target := range aliases {
		if name == "" || target == "" {
			continue
		}
		out[name] = target
	}
	return out
}
