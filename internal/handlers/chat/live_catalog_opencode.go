package chat

import (
	"context"
	"io"
	"net/http"
	"strings"

	json "encoding/json/v2"

	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
)

const opencodeLiveCatalogURL = "https://opencode.ai/zen/v1/models"

func isOpencodeFreeLiveModel(id string) bool {
	if id == "big-pickle" {
		return true
	}
	if id == "deepseek-v4-flash-free" {
		return false
	}
	return strings.HasSuffix(id, "-free")
}

func (h *ChatHandler) fetchOpenCodeLiveCatalog(ctx context.Context, conn *models.ProviderConnection, connData *shared.ConnectionData) []LiveModel {
	reqCtx, cancel := context.WithTimeout(ctx, liveCatalogFastHTTP)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, opencodeLiveCatalogURL, nil)
	if err != nil {
		return nil
	}
	headers := map[string]string{
		"x-opencode-client": "desktop",
		"User-Agent":        "curl/7.68.0",
	}
	for k, v := range handlerutil.WithDefaultUserAgent(headers) {
		req.Header.Set(k, v)
	}

	resp, err := h.Client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil
	}

	var payload struct {
		Models []map[string]any `json:"models"`
		Data   []map[string]any `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	return parseOpenCodeLiveModels(body)
}

func parseOpenCodeLiveModels(body []byte) []LiveModel {
	var payload struct {
		Models []map[string]any `json:"models"`
		Data   []map[string]any `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	rawList := payload.Models
	if len(rawList) == 0 {
		rawList = payload.Data
	}

	var out []LiveModel
	seen := make(map[string]bool, len(rawList))
	for _, m := range rawList {
		id := firstNonEmptyString(m, "id", "name")
		if id == "" || seen[id] || !isOpencodeFreeLiveModel(id) {
			continue
		}
		seen[id] = true

		ctxLen := 0
		maxOut := 0
		if lim, ok := m["limit"].(map[string]any); ok {
			ctxLen = firstPositiveInt(lim, "context", "context_length")
			maxOut = firstPositiveInt(lim, "output", "max_output")
		}

		caps := &LiveCapabilities{}
		if c, ok := m["capabilities"].(map[string]any); ok {
			if r, ok := c["reasoning"].(map[string]any); ok {
				if sup, ok := r["supported"].(bool); ok && sup {
					caps.Thinking = true
				}
			}
		}

		out = append(out, LiveModel{
			ID:                  id,
			Name:                firstNonEmptyString(m, "name", "id"),
			ContextLength:       ctxLen,
			MaxOutput:           maxOut,
			Capabilities:        caps,
			LimitsAuthoritative: ctxLen > 0 || maxOut > 0,
		})
	}
	return out
}
