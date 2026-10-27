package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"encoding/json/jsontext"
	json "encoding/json/v2"

	"9router/proxy/internal/customjson"
	"9router/proxy/internal/db"
)

// customJSONProbePrompt is the user text the explicit "Send Test Request"
// sends. Saving a node or a connection never calls the endpoint; only this
// test does (upstream decolua/9router#4929).
const customJSONProbePrompt = "ping"

// nodeCustomJSON validates the custom JSON configuration a create or update
// carries. It returns nil for every node that is not a custom JSON one, which
// is how switching a node back to Chat Completions drops its template.
func nodeCustomJSON(nodeType, apiType string, raw jsontext.Value, endpoint string) (jsontext.Value, error) {
	if nodeType != "openai-compatible" || apiType != customjson.APIType {
		return nil, nil
	}
	cfg, err := customjson.Normalize(raw, endpoint)
	if err != nil {
		return nil, err
	}
	return cfg.Marshal(), nil
}

// storedCustomJSON is the configuration a node row reports, or nil when the
// node is not a custom JSON one (a stale template left in the blob of a node
// switched away from custom JSON is never surfaced).
func storedCustomJSON(apiType string, raw jsontext.Value) jsontext.Value {
	if apiType != customjson.APIType || len(raw) == 0 || raw.Kind() != '{' {
		return nil
	}
	return raw
}

// nodeDataBlob is the providerNodes.data JSON for a compatible node.
func nodeDataBlob(prefix, apiType, baseURL string, customJSON jsontext.Value) map[string]any {
	data := map[string]any{"prefix": prefix, "apiType": apiType, "baseUrl": baseURL}
	if len(customJSON) > 0 {
		data["customJson"] = customJSON
	}
	return data
}

// nodeSync is what an edited node pushes into each attached connection's
// providerSpecificData.
type nodeSync struct {
	prefix, apiType, nodeType, baseURL, nodeName string
	customJSON                                   jsontext.Value
}

// setConnCustomJSON copies a node's configuration into a connection's
// providerSpecificData, or removes it when the node has none.
func setConnCustomJSON(psd map[string]any, customJSON jsontext.Value) {
	if len(customJSON) == 0 {
		delete(psd, "customJson")
		return
	}
	var decoded map[string]any
	if json.Unmarshal(customJSON, &decoded) != nil {
		delete(psd, "customJson")
		return
	}
	psd["customJson"] = decoded
}

// rawAuthType reads the auth type of a not yet validated configuration, so a
// no-auth endpoint can be tested without an API key.
func rawAuthType(raw jsontext.Value) customjson.AuthType {
	var head struct {
		AuthType customjson.AuthType `json:"authType"`
	}
	_ = json.Unmarshal(raw, &head)
	return head.AuthType
}

// probeDoFunc performs one outbound probe request.
type probeDoFunc func(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error)

// probeCustomJSON sends one real request to a custom JSON endpoint, exactly
// as the router would for a "ping" prompt, and reports whether the reply
// could be read at the configured response path. do is validateProbeDo for
// node validation, or the connection's own proxied client for a connection
// test.
func probeCustomJSON(ctx context.Context, do probeDoFunc, endpoint string, raw jsontext.Value, apiKey, model string) map[string]any {
	if model == "" {
		model = "custom"
	}
	cfg, err := customjson.Normalize(raw, endpoint)
	if err != nil {
		return map[string]any{"valid": false, "error": err.Error()}
	}
	body, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": customJSONProbePrompt}},
		"stream":   false,
	})
	payload, err := customjson.BuildRequest(cfg, model, body)
	if err != nil {
		return map[string]any{"valid": false, "error": err.Error()}
	}
	headers, err := customjson.Headers(cfg, apiKey)
	if err != nil {
		return map[string]any{"valid": false, "error": err.Error()}
	}
	status, resBody, err := do(ctx, cfg.Method, endpoint, headers, payload)
	if err != nil {
		return map[string]any{"valid": false, "error": validateNodeNetworkMessage(err)}
	}
	if status < 200 || status >= 300 {
		return map[string]any{"valid": false, "error": fmt.Sprintf("Custom JSON request failed (%d): %s", status, truncateRunes(string(resBody), 300))}
	}
	if _, err := customjson.ExtractText(cfg, resBody); err != nil {
		return map[string]any{"valid": false, "error": "Custom JSON response: " + err.Error()}
	}
	return map[string]any{"valid": true, "method": customjson.APIType}
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// validateCustomJSONConnection answers /api/providers/validate for a custom
// JSON node. Saving a connection sends validateOnly and only re-checks the
// node's configuration; a real request goes out only for an explicit test.
func validateCustomJSONConnection(ctx context.Context, nodeData *db.ProviderNodeData, apiKey, modelID string, validateOnly bool) map[string]any {
	baseURL := strings.TrimSpace(nodeData.BaseURL)
	if validateOnly {
		if _, err := customjson.Normalize(nodeData.CustomJSON, baseURL); err != nil {
			return map[string]any{"valid": false, "supported": true, "error": err.Error()}
		}
		return map[string]any{"valid": true, "supported": true, "error": nil}
	}
	out := probeCustomJSON(ctx, validateProbeDo, baseURL, nodeData.CustomJSON, apiKey, modelID)
	out["supported"] = true
	return out
}

// probeCustomJSONConnection is the connection test for a custom JSON node,
// sent through the connection's own (possibly proxied) client.
func probeCustomJSONConnection(ctx context.Context, data connectionProbeData, client *http.Client) probeOutcome {
	psd := data.ProviderSpecificData
	raw, err := json.Marshal(psd["customJson"])
	if err != nil {
		return probeOutcome{message: "Invalid custom JSON configuration"}
	}
	do := func(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error) {
		return connectionProbeDo(ctx, client, method, rawURL, headers, body)
	}
	out := probeCustomJSON(ctx, do, psdStr(psd, "baseUrl"), raw, data.APIKey, data.DefaultModel)
	if valid, _ := out["valid"].(bool); valid {
		return probeOutcome{valid: true}
	}
	msg, _ := out["error"].(string)
	return probeOutcome{message: msg}
}
