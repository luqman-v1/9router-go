package handlers

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/pricing"
	"9router/proxy/internal/proxy"
)

// suggestedModel mirrors the shape returned by the Next.js
// /api/providers/suggested-models endpoint.
type suggestedModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength *int64 `json:"contextLength,omitempty"`
}

// Port of the FILTERS map in
// src/app/api/providers/suggested-models/filters.js (upstream 9router).
var (
	knownFreeOpencodeModels = pricing.KnownFreeModels
	deadFreeOpencodeModels  = map[string]bool{"deepseek-v4-flash-free": true}
)

func getString(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func getFloat(m map[string]any, key string) (float64, bool) {
	switch v := m[key].(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}

func getBool(m map[string]any, key string) bool {
	if b, ok := m[key].(bool); ok {
		return b
	}
	return false
}

func filterOpenRouterFree(models []map[string]any) []suggestedModel {
	out := []suggestedModel{}
	for _, m := range models {
		pricing, _ := m["pricing"].(map[string]any)
		clen, ok := getFloat(m, "context_length")
		if !ok || clen < 200000 {
			continue
		}
		if pricing == nil || getString(pricing, "prompt") != "0" || getString(pricing, "completion") != "0" {
			continue
		}
		n := int64(clen)
		out = append(out, suggestedModel{ID: getString(m, "id"), Name: getString(m, "name"), ContextLength: &n})
	}
	sort.Slice(out, func(i, j int) bool {
		var a, b int64
		if out[i].ContextLength != nil {
			a = *out[i].ContextLength
		}
		if out[j].ContextLength != nil {
			b = *out[j].ContextLength
		}
		return a > b
	})
	return out
}

func filterOpencodeFree(models []map[string]any) []suggestedModel {
	out := []suggestedModel{}
	for _, m := range models {
		id := getString(m, "id")
		if id == "" || deadFreeOpencodeModels[id] {
			continue
		}
		free := strings.HasSuffix(id, "-free")
		if !free {
			for _, k := range knownFreeOpencodeModels {
				if id == k {
					free = true
					break
				}
			}
		}
		if !free {
			continue
		}
		out = append(out, suggestedModel{ID: id, Name: id})
	}
	return out
}

func filterMimoFree(models []map[string]any) []suggestedModel {
	out := []suggestedModel{}
	for _, m := range models {
		id := getString(m, "id")
		name := getString(m, "name")
		if id == "" {
			continue
		}
		if !strings.HasPrefix(id, "mimo") && !strings.Contains(strings.ToLower(name), "mimo") {
			continue
		}
		if name == "" {
			name = id
		}
		out = append(out, suggestedModel{ID: id, Name: name})
	}
	return out
}

func filterAirforceFree(models []map[string]any) []suggestedModel {
	out := []suggestedModel{}
	for _, m := range models {
		id := getString(m, "id")
		if id == "" {
			continue
		}
		free := getString(m, "tier") == "free" || strings.HasSuffix(id, ":free")
		if !free || !getBool(m, "supports_chat") {
			continue
		}
		if mt := getString(m, "media_type"); mt != "" && mt != "chat" && mt != "text" {
			continue
		}
		name := getString(m, "name")
		if name == "" {
			name = id
		}
		sm := suggestedModel{ID: id, Name: name}
		if clen, ok := getFloat(m, "context_length"); ok {
			n := int64(clen)
			sm.ContextLength = &n
		}
		out = append(out, sm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// filterMinimaxCode reads MiniMax Code's live catalogue. Its shape is an
// object, not a model list:
//
//	{providers:[{providerId:"minimax", config:{models:{<id>:{name, limit, …}}}}]}
//
// so the payload is unwrapped here rather than in the fetch, where every other
// provider's filter receives a flat array.
func filterMinimaxCode(envelopes []map[string]any) []suggestedModel {
	for _, env := range envelopes {
		providers, _ := env["providers"].([]any)
		for _, raw := range providers {
			p, ok := raw.(map[string]any)
			if !ok || getString(p, "providerId") != "minimax" {
				continue
			}
			config, _ := p["config"].(map[string]any)
			models, _ := config["models"].(map[string]any)
			out := make([]suggestedModel, 0, len(models))
			for id, rawModel := range models {
				m, ok := rawModel.(map[string]any)
				if !ok || id == "" {
					continue
				}
				sm := suggestedModel{ID: id, Name: getString(m, "name")}
				if sm.Name == "" {
					sm.Name = id
				}
				if limit, ok := m["limit"].(map[string]any); ok {
					if clen, ok := getFloat(limit, "context"); ok && clen > 0 {
						n := int64(clen)
						sm.ContextLength = &n
					}
				}
				out = append(out, sm)
			}
			sort.Slice(out, func(i, j int) bool { return contextLengthOf(out[i]) > contextLengthOf(out[j]) })
			return out
		}
	}
	return []suggestedModel{}
}

// contextLengthOf reads an optional context window for sorting.
func contextLengthOf(m suggestedModel) int64 {
	if m.ContextLength == nil {
		return 0
	}
	return *m.ContextLength
}

// HandleSuggestedModels serves GET /api/providers/suggested-models?url=..&type=..,
// proxying a provider's public model catalog through one of the upstream
// FILTERS and returning { data: [...] } (empty array on any failure).
func HandleSuggestedModels(w http.ResponseWriter, r *http.Request) {
	feedURL := r.URL.Query().Get("url")
	filterType := r.URL.Query().Get("type")
	if feedURL == "" || filterType == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing url or type")
		return
	}

	var filter func([]map[string]any) []suggestedModel
	switch filterType {
	case "openrouter-free":
		filter = filterOpenRouterFree
	case "opencode-free":
		filter = filterOpencodeFree
	case "mimo-free":
		filter = filterMimoFree
	case "airforce-free":
		filter = filterAirforceFree
	case "minimax-code":
		filter = filterMinimaxCode
	default:
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Unknown filter type")
		return
	}

	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: proxy.NewFallbackTransport(http.DefaultTransport),
	}
	resp, err := client.Get(feedURL)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"data": []suggestedModel{}})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"data": []suggestedModel{}})
		return
	}

	var payload any
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil || json.Unmarshal(body, &payload) != nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"data": []suggestedModel{}})
		return
	}
	var raw []any
	if obj, ok := payload.(map[string]any); ok {
		if d, ok := obj["data"].([]any); ok {
			raw = d
		} else if d, ok := obj["models"].([]any); ok {
			raw = d
		} else if _, isEnvelope := obj["providers"]; isEnvelope {
			// MiniMax Code's catalogue is an object, not a model list. It rides
			// in as a single-element array so every filter keeps the same
			// array-in contract and only this one unwraps it.
			raw = []any{obj}
		}
	} else if arr, ok := payload.([]any); ok {
		raw = arr
	}
	models := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			models = append(models, m)
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"data": filter(models)})
}
