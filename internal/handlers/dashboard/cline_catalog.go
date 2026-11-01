package dashboard

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/updater"
)

// Cline / ClinePass model discovery.
//
// Cline publishes no OpenAI-style `/models` route, so the generic branch answered
// "provider cline does not support models listing" and the dashboard's fetch
// button was dead. It does expose two live catalogues on the same host:
//
//	GET /api/v1/ai/cline/models               -> the full catalogue
//	GET /api/v1/ai/cline/recommended-models   -> {recommended, free, clinePass, clineCloud}
//
// Both take the WorkOS-prefixed bearer token and the Cline client-identity
// headers. The two namespaces are kept deliberately disjoint, which is what
// stops Import advertising a subscription-only model on the free Cline
// connection (which answers 403 ENTITLEMENT_ERROR) or the reverse:
//
//   - `cline`     = the full catalogue, minus the `cline-pass/*` subscription
//     namespace and minus non-text-output rows (embedding/rerank entries cannot
//     serve a chat completion).
//   - `clinepass` = ONLY the `cline-pass/*` rows, from the recommended
//     endpoint's clinePass bucket.
//
// The URLs are vars so a test can repoint them at an httptest server;
// production never mutates them.
var (
	clineModelsURL            = "https://api.cline.bot/api/v1/ai/cline/models"
	clineRecommendedModelsURL = "https://api.cline.bot/api/v1/ai/cline/recommended-models"
)

// clineModelItem is one row of the catalogue as the dashboard renders it. It
// matches the shape connections.go emits for every other provider, so the
// frontend needs no Cline-specific branch.
type clineModelItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// clineCatalogTimeout bounds the live catalogue fetch. The dashboard's fetch
// button is a person waiting on it, so this is deliberately short; a slow
// catalogue falls back to the offline list rather than hanging the request.
const clineCatalogTimeout = 15 * time.Second

// clinePassModelPrefix is the ClinePass subscription namespace.
const clinePassModelPrefix = "cline-pass/"

// clineStaticFallbackModels is the offline catalogue, used only when live
// discovery is unreachable, so Import degrades to a useful list instead of
// failing. ClinePass ids are deliberately absent: that namespace is served by
// the sibling provider and an id here would be offered on the wrong connection.
var clineStaticFallbackModels = []string{
	"cline-free/deepseek-v4.1-flash",
	"cline-free/muse-spark-1.3-contributor",
	"stealth/union-alpha",
	"z-ai/glm-5.3-flash",
	"openai/gpt-6-astra",
	"moonshotai/kimi-k3",
	"anthropic/claude-opus-5",
	"x-ai/grok-4.5",
	"deepseek/deepseek-v4-flash",
	"google/gemini-3.1-pro-preview",
}

// clineExtraCatalogRows lists ids the gateway demonstrably serves but the live
// catalogue omits. Probed on a live account against POST /api/v1/chat/completions:
// the gateway answers 402 (auth accepted, billing) for these ids while unknown
// ids get 404 "model not found" — so they are real, they just dropped out of the
// listing. cline-free/deepseek-v4.1-flash answers 200 with content, yet the
// listing carries zero cline-free/* rows.
//
// A row already present in the live catalogue is kept as-is (no override).
var clineExtraCatalogRows = []string{
	"cline-free/deepseek-v4.1-flash",
	"cline-free/muse-spark-1.3-contributor",
	"google/pixel-canary",
	"openrouter/pixel-canary",
}

// isClineProvider reports whether the provider id is the free Cline connection.
func isClineProvider(providerID string) bool {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "cline", "cl":
		return true
	default:
		return false
	}
}

// isClinePassProvider reports whether the provider id is the ClinePass
// subscription connection. They share a host and auth contract, so they are
// resolved together and differ only in which namespace they expose.
func isClinePassProvider(providerID string) bool {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "clinepass", "cline-pass", "cp":
		return true
	default:
		return false
	}
}

// isClineFamily reports whether the provider belongs to either Cline namespace.
func isClineFamily(providerID string) bool {
	return isClineProvider(providerID) || isClinePassProvider(providerID)
}

// clineCanonical names the provider the catalogue is reported under, so a
// connection saved under an alias still reports the canonical id.
func clineCanonical(providerID string) string {
	if isClinePassProvider(providerID) {
		return "clinepass"
	}
	return "cline"
}

// fetchClineModels resolves the catalogue for a Cline / ClinePass connection.
//
// Every failure path falls back to the offline catalogue with a Warning rather
// than returning an error: the Import button completes and the operator sees why
// the list is static, which is more useful than a dead button.
func fetchClineModels(providerID, data string, timeout time.Duration) ([]clineModelItem, string, error) {
	canonical := clineCanonical(providerID)
	isPass := canonical == "clinepass"

	token := clineTokenFromData(data)
	if token == "" {
		return clineFallbackModels(isPass), "No Cline credential on this connection — using the built-in fallback catalogue.", nil
	}
	raw, status, err := getClineCatalog(clineEndpointFor(isPass), token, timeout)
	if err != nil {
		return clineFallbackModels(isPass), "Live Cline catalogue unreachable (" + err.Error() + ") — using the built-in fallback catalogue.", nil
	}
	if status != http.StatusOK {
		return clineFallbackModels(isPass), "Cline catalogue returned HTTP " + strconv.Itoa(status) + " — using the built-in fallback catalogue.", nil
	}
	items := parseClineCatalog(raw, isPass)
	if len(items) == 0 {
		return clineFallbackModels(isPass), "Cline catalogue contained no usable chat models — using the built-in fallback catalogue.", nil
	}
	items = mergeClineExtraCatalogRows(items, isPass)
	sortClineModels(items)
	return items, "", nil
}

// clineTokenFromData reads the Cline credential out of a connection's data
// blob. Both spellings exist in the wild: accessToken for OAuth connections and
// apiKey for a ClinePass key pasted into the same field.
func clineTokenFromData(data string) string {
	var parsed struct {
		AccessToken string `json:"accessToken"`
		APIKey      string `json:"apiKey"`
	}
	if err := json.Unmarshal([]byte(data), &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(firstNonEmpty(parsed.AccessToken, parsed.APIKey))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// mergeClineExtraCatalogRows appends the known-but-unlisted ids unless already
// present. ClinePass has its own subscription namespace, so the rows are not
// offered there.
func mergeClineExtraCatalogRows(items []clineModelItem, isPass bool) []clineModelItem {
	if isPass {
		return items
	}
	have := make(map[string]bool, len(items))
	for _, m := range items {
		have[m.ID] = true
	}
	for _, id := range clineExtraCatalogRows {
		if have[id] {
			continue
		}
		items = append(items, clineModelItem{ID: id, Name: id})
		have[id] = true
	}
	return items
}

// clineEndpointFor picks the catalogue endpoint for the namespace. The full
// catalogue backs `cline`; ClinePass uses the recommended endpoint because its
// subscription bucket is only published there.
func clineEndpointFor(isPass bool) string {
	if isPass {
		return clineRecommendedModelsURL
	}
	return clineModelsURL
}

// getClineCatalog performs the authenticated catalogue GET with Cline's client
// identity headers.
//
// The token is normalized to the `workos:` form here, so a connection saved
// before that normalization still imports correctly. ClinePass keys ride plain
// Bearer — NormalizeProviderToken makes exactly that distinction.
func getClineCatalog(url, token string, timeout time.Duration) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", clineAuthHeader(token))
	req.Header.Set("Accept", "application/json")
	for k, v := range providers.ClineIdentityHeaders(updater.CurrentVersion, nil) {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

// clineCatalogRow covers both endpoint shapes; the full catalogue and the
// recommended buckets share the same id/name fields.
type clineCatalogRow struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Architecture *struct {
		Modality         string   `json:"modality"`
		OutputModalities []string `json:"output_modalities"`
	} `json:"architecture"`
}

type clineRecommendedPayload struct {
	Recommended []clineCatalogRow `json:"recommended"`
	Free        []clineCatalogRow `json:"free"`
	ClinePass   []clineCatalogRow `json:"clinePass"`
	ClineCloud  []clineCatalogRow `json:"clineCloud"`
}

// parseClineCatalog turns a catalogue body into model rows.
//
// A model is admitted on the full catalogue when it does not explicitly declare a
// non-text output modality. Rows that omit architecture entirely are kept, so an
// upstream schema change cannot silently empty the catalogue — a shorter list is
// recoverable, an empty one is not.
func parseClineCatalog(raw []byte, isPass bool) []clineModelItem {
	var rows []clineCatalogRow
	if isPass {
		var payload clineRecommendedPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil
		}
		rows = payload.ClinePass
	} else {
		// The full catalogue may be a bare array or wrapped in {"data":[...]}.
		var wrapped struct {
			Data []clineCatalogRow `json:"data"`
		}
		if err := json.Unmarshal(raw, &wrapped); err == nil && len(wrapped.Data) > 0 {
			rows = wrapped.Data
		} else if err := json.Unmarshal(raw, &rows); err != nil {
			return nil
		}
	}
	seen := make(map[string]bool, len(rows))
	out := make([]clineModelItem, 0, len(rows))
	for _, row := range rows {
		id := strings.TrimSpace(row.ID)
		if id == "" || seen[id] {
			continue
		}
		if isPass {
			if !strings.HasPrefix(id, clinePassModelPrefix) {
				continue
			}
		} else {
			// ClinePass rows belong to the sibling provider.
			if strings.HasPrefix(id, clinePassModelPrefix) {
				continue
			}
			if !clineHasTextOutput(row) {
				continue
			}
		}
		seen[id] = true
		out = append(out, clineModelItem{ID: id, Name: clineDisplayName(row)})
	}
	return out
}

// clineDisplayName prefers the human-readable name, falling back to the id.
func clineDisplayName(row clineCatalogRow) string {
	if n := strings.TrimSpace(row.Name); n != "" {
		return n
	}
	return strings.TrimSpace(row.ID)
}

// clineHasTextOutput reports whether the row can serve a chat completion. Rows
// without architecture metadata are admitted (fail-open) so a schema change
// degrades to a longer list rather than an empty one.
func clineHasTextOutput(row clineCatalogRow) bool {
	if row.Architecture == nil {
		return true
	}
	if outs := row.Architecture.OutputModalities; len(outs) > 0 {
		for _, m := range outs {
			if strings.EqualFold(strings.TrimSpace(m), "text") {
				return true
			}
		}
		return false
	}
	// Legacy shape: "text+image->text". Only the output side matters.
	if modality := row.Architecture.Modality; modality != "" {
		if _, out, ok := strings.Cut(modality, "->"); ok {
			return strings.EqualFold(strings.TrimSpace(out), "text")
		}
	}
	return true
}

// sortClineModels orders by id so the list is stable across fetches; the upstream
// order is not documented and changes without notice.
func sortClineModels(items []clineModelItem) {
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
}

// clineAuthHeader applies the workos: prefix rule the same way
// chat.NormalizeProviderToken does: a WorkOS JWT rides the prefix, a ClinePass
// API key (clp_...) rides plain Bearer. Upstream parity:
// open-sse/shared/clineAuth.js getClineAccessToken.
func clineAuthHeader(token string) string {
	t := strings.TrimSpace(token)
	if !strings.HasPrefix(t, "workos:") && isClineWorkOSJWT(t) {
		return "workos:" + t
	}
	return t
}

// clineFallbackModels returns the offline catalogue for the namespace. ClinePass
// has no meaningful offline subset — its ids are subscription-gated and would be
// offered on a connection that answers 403 — so it resolves to an empty list and
// the Warning explains why.
func clineFallbackModels(isPass bool) []clineModelItem {
	if isPass {
		return []clineModelItem{}
	}
	out := make([]clineModelItem, 0, len(clineStaticFallbackModels))
	for _, id := range clineStaticFallbackModels {
		out = append(out, clineModelItem{ID: id, Name: id})
	}
	sortClineModels(out)
	return out
}

// filterClineNamespace keeps the two Cline namespaces disjoint on the live
// catalogue rows.
//
//   - clinepass keeps ONLY the cline-pass/* rows.
//   - cline keeps everything EXCEPT cline-pass/*.
//
// Rows that are not objects (an unexpected upstream shape) are dropped rather
// than passed through, because the caller merges them by id and a non-object
// would silently break that merge.
func filterClineNamespace(rows []any, isPass bool) []any {
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		isPassRow := strings.HasPrefix(id, clinePassModelPrefix)
		if isPass == isPassRow {
			out = append(out, r)
		}
	}
	return out
}
