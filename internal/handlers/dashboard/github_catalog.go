package dashboard

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"sort"
	"strings"

	"9router/proxy/internal/providers"
)

// GitHub Copilot model catalogue.
//
// Copilot's /models answer is not a plain id list: every entry carries a
// `capabilities` block and a `policy` block, and the account's entitlement is
// expressed through them. The filter below is capability-driven rather than an
// id allow-list, so a model the account becomes entitled to tomorrow appears
// with no code change.
//
// Two filters are deliberately NOT applied, both learned against a live
// account:
//
//   - `model_picker_enabled` means "show this in the editor's model picker",
//     not "you may call this". Filtering on it collapsed a real catalogue from
//     30 models to 0 — the account was entitled to 9 models that all reported
//     model_picker_enabled: false.
//   - The id is not filtered at all. A retired id that still answers is better
//     offered and failed on than silently hidden.
//
// `policy.state` IS authoritative: anything other than "enabled" is dropped.

// CopilotModel is one catalogue entry as the dashboard renders it.
type CopilotModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	IsReasoning bool   `json:"isReasoning,omitempty"`
}

// isGitHubProvider reports whether providerID is a GitHub Copilot provider id
// this catalogue handler serves.
func isGitHubProvider(providerID string) bool {
	return providerID == "github"
}

// copilotCatalogItem is one entry of Copilot's /models answer. Only the fields
// this handler reads are declared; an unknown key is ignored rather than
// failing the decode, so a catalogue schema change degrades to a longer list
// instead of an empty one.
type copilotCatalogItem struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Model              string `json:"model"`
	SupportedEndpoints []struct {
		Endpoint string `json:"endpoint"`
	} `json:"supported_endpoints"`
	Capabilities struct {
		Type     string `json:"type"`
		Family   string `json:"family"`
		Limits   any    `json:"limits"`
		Supports struct {
			Reasoning any `json:"reasoning"`
		} `json:"supports"`
	} `json:"capabilities"`
	Policy struct {
		State string `json:"state"`
	} `json:"policy"`
}

// isRoutableCopilotChatModel reports whether a catalogue entry is a chat model
// this account may call.
//
// The capability check is the primary gate: `capabilities.type == "chat"`. When
// the block is absent entirely (older catalogue shape), the entry is kept and
// the chat shape is confirmed by a `supported_endpoints` fallback.
func isRoutableCopilotChatModel(item copilotCatalogItem) bool {
	id := strings.TrimSpace(item.ID)
	if id == "" {
		id = strings.TrimSpace(item.Model)
	}
	if id == "" {
		return false
	}
	// policy.state is authoritative.
	if state := strings.ToLower(strings.TrimSpace(item.Policy.State)); state != "" && state != "enabled" {
		return false
	}
	kind := strings.ToLower(strings.TrimSpace(item.Capabilities.Type))
	if kind != "" {
		return kind == "chat"
	}
	// No capability block: fall back to a chat-shaped endpoint list.
	for _, ep := range item.SupportedEndpoints {
		if strings.Contains(strings.ToLower(ep.Endpoint), "chat") {
			return true
		}
	}
	return false
}

// copilotModelID returns the entry's id, tolerating the `model` spelling.
func copilotModelID(item copilotCatalogItem) string {
	if id := strings.TrimSpace(item.ID); id != "" {
		return id
	}
	return strings.TrimSpace(item.Model)
}

// copilotTokenFromData returns the credential a catalogue call must send.
//
// The derived Copilot bearer is preferred over the GitHub access token: a
// stored `copilotToken` is the credential /models actually accepts, and it is
// refreshed by the same path that refreshes chat. The GitHub token is retained
// as the refresh input, not as the request credential.
func copilotTokenFromData(data string) string {
	if strings.TrimSpace(data) == "" {
		return ""
	}
	var conn struct {
		AccessToken          string `json:"accessToken"`
		APIKey               string `json:"apiKey"`
		ProviderSpecificData struct {
			CopilotToken string `json:"copilotToken"`
		} `json:"providerSpecificData"`
	}
	if err := json.Unmarshal([]byte(data), &conn); err != nil {
		return ""
	}
	if t := strings.TrimSpace(conn.ProviderSpecificData.CopilotToken); t != "" {
		return t
	}
	if t := strings.TrimSpace(conn.AccessToken); t != "" {
		return t
	}
	return strings.TrimSpace(conn.APIKey)
}

// parseCopilotCatalog decodes a /models body into CopilotModel rows, keeping
// only routable chat entries.
//
// Copilot has shipped both a bare array and a `data`-wrapped array, so both are
// read.
func parseCopilotCatalog(raw []byte) []CopilotModel {
	if len(raw) == 0 {
		return nil
	}
	var items []copilotCatalogItem
	if err := json.Unmarshal(raw, &items); err != nil {
		var env struct {
			Data []copilotCatalogItem `json:"data"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil
		}
		items = env.Data
	}
	out := make([]CopilotModel, 0, len(items))
	for _, item := range items {
		if !isRoutableCopilotChatModel(item) {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = copilotModelID(item)
		}
		out = append(out, CopilotModel{
			ID:          copilotModelID(item),
			Name:        name,
			IsReasoning: item.Capabilities.Family != "",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// fetchCopilotCatalogModels calls Copilot's /models with the CLI identity and
// returns the routable chat entries.
//
// The call is made with the same headers the inference path sends, because the
// catalogue the account is entitled to depends on the client identity it is
// asked as.
func fetchCopilotCatalogModels(ctx context.Context, data string) ([]CopilotModel, error) {
	token := copilotTokenFromData(data)
	if token == "" {
		return nil, errCopilotNoCredential
	}
	headers := providers.GitHubCopilotChatHeaders("application/json", "user")
	headers["Authorization"] = "Bearer " + token

	status, body, err := validateProbeDo(ctx, http.MethodGet, "https://api.githubcopilot.com/models", headers, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, &upstreamStatusError{Status: status, Body: strings.TrimSpace(string(body))}
	}
	return parseCopilotCatalog(body), nil
}

// errCopilotNoCredential is returned when a connection carries neither a
// derived Copilot token nor a GitHub access token.
var errCopilotNoCredential = &upstreamStatusError{Status: http.StatusUnauthorized, Body: "no Copilot credential found"}

// upstreamStatusError carries an upstream status and body so callers can map it
// onto the same status the dashboard reports for every other provider.
type upstreamStatusError struct {
	Status int
	Body   string
}

func (e *upstreamStatusError) Error() string {
	if e.Body == "" {
		return "upstream returned " + http.StatusText(e.Status)
	}
	return e.Body
}
