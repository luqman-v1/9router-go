package chat

// Live catalogue resolution for the Command Code CLI provider.
//
// GET https://api.commandcode.ai/provider/v1/models?view=client is the feed the
// official CLI reads: it is the plain OpenAI /v1/models list plus the fields the
// CLI renders — display.label, reasoningEfforts, context_length — and, crucially,
// a per-model `hidden` flag. A hidden model is still served to older clients but
// is no longer offered (minimax-m3-free, reflection/beam:free, the stealth
// canaries), so publishing it here would put models in the picker that the
// provider has withdrawn.
//
// The endpoint validates CLI identity, not the key: with no User-Agent: cli it
// answers 403 whether or not a bearer token is sent, and a bogus `Bearer
// user_fake` answers 200. The header set therefore comes from the registry's
// commandcode entry (the same one the chat lane dials with) instead of being
// restated here, so a CLI version bump cannot reach one path and miss the other.

import (
	"context"
	"io"
	"net/http"

	json "encoding/json/v2"

	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

const (
	commandcodeCatalogProvider = "commandcode"
	commandcodeCatalogView     = "view=client"
)

// commandcodeCatalogURL is a variable so tests can point the resolver at a
// local server. The base comes from the registry map the dashboard uses, so the
// two discovery paths cannot drift onto different endpoints.
var commandcodeCatalogURL = providers.ModelsListURL(commandcodeCatalogProvider) + "?" + commandcodeCatalogView

// commandcodeCatalogHeaders builds the request header set: the registry's CLI
// identity, plus the bearer when the connection carries one.
func commandcodeCatalogHeaders(apiKey string) map[string]string {
	static := providers.KnownProviders[commandcodeCatalogProvider].StaticHeaders
	headers := make(map[string]string, len(static)+2)
	for k, v := range static {
		headers[k] = v
	}
	headers["Accept"] = "application/json"
	if apiKey != "" {
		headers["Authorization"] = "Bearer " + apiKey
	}
	return headers
}

func (h *ChatHandler) fetchCommandcodeLiveCatalog(ctx context.Context, conn *models.ProviderConnection, apiKey string) []LiveModel {
	reqCtx, cancel := context.WithTimeout(ctx, liveCatalogFastHTTP)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, commandcodeCatalogURL, nil)
	if err != nil {
		return nil
	}
	for key, value := range commandcodeCatalogHeaders(apiKey) {
		req.Header.Set(key, value)
	}

	resp, err := h.Client.Do(req)
	if err != nil {
		log.Warn("models", "commandcode catalog fetch failed", "conn", conn.ID, "error", err)
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		log.Warn("models", "commandcode catalog rejected", "conn", conn.ID, "status", resp.StatusCode)
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil
	}
	return parseCommandcodeLiveModels(body)
}

// parseCommandcodeLiveModels keeps the models the provider still offers, in
// feed order.
//
// context_length is published as authoritative because it is the provider's own
// enforced window and the static commandcode table answers 1M for the whole
// catalogue while the feed spans 200k–1.05M. MaxOutput is deliberately left
// unset: the chat lane rejects params.max_tokens above 200k regardless of what
// the feed advertises for individual models, so the static cap stays the one
// clients see.
func parseCommandcodeLiveModels(body []byte) []LiveModel {
	var payload struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			Hidden        bool   `json:"hidden"`
			ContextLength int    `json:"context_length"`
			Display       struct {
				Label string `json:"label"`
			} `json:"display"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}

	seen := make(map[string]bool, len(payload.Data))
	out := make([]LiveModel, 0, len(payload.Data))
	for _, entry := range payload.Data {
		if entry.Hidden || entry.ID == "" || seen[entry.ID] {
			continue
		}
		seen[entry.ID] = true
		name := entry.Display.Label
		if name == "" {
			name = entry.Name
		}
		out = append(out, LiveModel{
			ID:                  entry.ID,
			Name:                name,
			ContextLength:       entry.ContextLength,
			LimitsAuthoritative: entry.ContextLength > 0,
		})
	}
	return out
}
