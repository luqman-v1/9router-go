package dashboard

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strings"

	"9router/proxy/internal/proxy/executor"
)

// Qoder live model list — port of open-sse/services/qoderModels.js
// (fetchQoderCatalogRaw) and the normalization its route applies.
//
// Qoder has no plain /models endpoint: the catalogue is served by the COSY
// gateway at /algo/api/v2/model/list and the request must be signed with the
// account's user id. Without this branch GET /api/providers/{id}/models
// answered "provider qoder does not support models listing" and the
// dashboard's fetch button was dead.

// qoderCatalogEntry is one {chat:[...]} record from the model list.
type qoderCatalogEntry struct {
	Key             string `json:"key"`
	DisplayName     string `json:"display_name"`
	MaxInputTokens  any    `json:"max_input_tokens"`
	MaxOutputTokens any    `json:"max_output_tokens"`
	IsVL            bool   `json:"is_vl"`
	IsReasoning     bool   `json:"is_reasoning"`
	// Enable is a pointer because upstream only hides a model when the field is
	// explicitly false; a missing field means "show it".
	Enable      *bool  `json:"enable"`
	Description string `json:"description"`
}

// QoderModel is the shape the dashboard and chat router expect.
type QoderModel struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ContextLength   int    `json:"contextLength"`
	IsVL            bool   `json:"isVL"`
	IsReasoning     bool   `json:"isReasoning"`
	MaxOutputTokens int    `json:"maxOutputTokens"`
	Description     string `json:"description"`
}

// qoderDefaultContextLength matches upstream's fallback when an entry omits
// max_input_tokens.
const qoderDefaultContextLength = 131_072

// fetchQoderCatalogModels returns the enabled models for a Qoder connection.
// token is the stored credential (dt-…, jt-… or pt-…); psd is the
// connection's providerSpecificData, which carries the signing user id.
//
// The endpoint table, PAT exchange and COSY signing live in the executor
// package: chat resolves model_config from the very same catalogue, so both
// paths must agree on which host a token kind is served from.
func fetchQoderCatalogModels(ctx context.Context, provider, token string, psd map[string]any) ([]QoderModel, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("Qoder credential is empty")
	}
	endpoints := executor.QoderEndpointsFor(provider)
	userID := psdStr(psd, "userId", "user_id", "id")
	do := qoderProbeDoer(ctx)

	// A PAT cannot sign COSY requests — trade it for a job token first.
	if executor.IsQoderPAT(token) {
		jobToken, err := executor.ExchangeQoderJobToken(ctx, do, endpoints.JobTokenExchangeURL, token)
		if err != nil {
			return nil, err
		}
		token = jobToken
		if userID == "" {
			userID = executor.FetchQoderUserID(ctx, do, endpoints.UserInfoURL, jobToken)
		}
	}
	if userID == "" {
		return nil, fmt.Errorf("Qoder user id is missing — re-authorize this connection")
	}

	modelListURL := endpoints.ModelListURLForToken(token)
	headers, err := executor.BuildQoderCosyHeaders(nil, modelListURL, executor.QoderCosyCreds{
		UserID:    userID,
		AuthToken: token,
		MachineID: psdStr(psd, "machineId", "machine_id"),
	})
	if err != nil {
		return nil, err
	}
	headers["Accept"] = "application/json"
	headers["Accept-Encoding"] = "identity"

	status, body, err := do(ctx, http.MethodGet, modelListURL, headers, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("Qoder model list returned %d", status)
	}

	var parsed struct {
		Chat []qoderCatalogEntry `json:"chat"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("Qoder model list was not JSON")
	}

	models := make([]QoderModel, 0, len(parsed.Chat))
	for _, entry := range parsed.Chat {
		if entry.Key == "" {
			continue
		}
		// Upstream caches the config for every key but surfaces only the ones
		// the account has enabled.
		if entry.Enable != nil && !*entry.Enable {
			continue
		}
		name := entry.DisplayName
		if name == "" {
			name = entry.Key
		}
		models = append(models, QoderModel{
			ID:              entry.Key,
			Name:            name,
			ContextLength:   qoderIntOr(entry.MaxInputTokens, qoderDefaultContextLength),
			IsVL:            entry.IsVL,
			IsReasoning:     entry.IsReasoning,
			MaxOutputTokens: qoderIntOr(entry.MaxOutputTokens, 0),
			Description:     entry.Description,
		})
	}
	return models, nil
}

func qoderIntOr(v any, fallback int) int {
	f, ok := usageFiniteNum(v)
	if !ok || f <= 0 {
		return fallback
	}
	return int(f)
}
