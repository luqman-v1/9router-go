//go:build integration

package integration

import (
	json "encoding/json/v2"
	"net/http"
	"testing"
)

func TestCodexExplicitLiteHeaderThroughRouter(t *testing.T) {
	tests := []struct {
		name, endpoint, header string
		search, lite           bool
	}{
		{"responses header", "/v1/responses", "true", false, true},
		{"chat header", "/v1/chat/completions", "true", false, true},
		{"false header", "/v1/responses", "false", false, false},
		{"hosted search wins over header", "/v1/responses", "true", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, upstream := codexResponsesEnv(t, nil)
			body := codexResponsesBody()
			if tt.endpoint == "/v1/chat/completions" {
				body = ChatBody("cx/gpt-6-luna", true)
			}
			if tt.search {
				body["tools"] = []any{map[string]any{"type": "web_search"}}
			}
			res := env.Post(t, tt.endpoint, body, WithHeader("X-OpenAI-Internal-Codex-Responses-Lite", tt.header))
			if res.Status != http.StatusOK {
				t.Fatalf("status = %d: %s", res.Status, res.Body)
			}
			sent := upstream.Last(t)
			var payload struct {
				Input []struct {
					Type string `json:"type"`
				} `json:"input"`
				Tools []struct {
					Type string `json:"type"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(sent.Body, &payload); err != nil {
				t.Fatal(err)
			}
			hasPrefix := false
			for _, item := range payload.Input {
				hasPrefix = hasPrefix || item.Type == "additional_tools"
			}
			if got := sent.Header.Get("X-OpenAI-Internal-Codex-Responses-Lite") == "true"; got != tt.lite || hasPrefix != tt.lite {
				t.Errorf("header=%v prefix=%v, want lite=%v: %s", got, hasPrefix, tt.lite, sent.Body)
			}
			if tt.search && (len(payload.Tools) != 1 || payload.Tools[0].Type != "web_search") {
				t.Errorf("hosted search lost: %s", sent.Body)
			}
		})
	}
}
