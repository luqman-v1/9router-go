//go:build integration

package integration

import (
	json "encoding/json/v2"
	"net/http"
	"strings"
	"testing"
)

func TestCodexPreservesNamedFunctionOutputThroughRouter(t *testing.T) {
	for _, lite := range []bool{false, true} {
		name, header := "classic", ""
		if lite {
			name, header = "explicit lite", "true"
		}
		t.Run(name, func(t *testing.T) {
			env, up := codexResponsesEnv(t, nil)
			body := codexResponsesBody()
			body["model"] = "cx/gpt-6.1-sol"
			body["input"] = []map[string]any{
				{"role": "user", "content": "Use the supplied tool context."},
				{"type": "function_call_output", "name": "lookup_fixture", "output": "synthetic fixture result"},
			}
			res := env.Post(t, "/v1/responses", body, WithHeader("x-openai-internal-codex-responses-lite", header))
			if res.Status != http.StatusOK || !strings.Contains(string(res.Body), `"response.completed"`) {
				t.Fatalf("named result did not complete: HTTP %d", res.Status)
			}
			var sent struct {
				Input []map[string]any `json:"input"`
			}
			if err := json.Unmarshal(up.Last(t).Body, &sent); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, item := range sent.Input {
				if item["type"] != "function_call_output" {
					continue
				}
				found = true
				if id, exists := item["call_id"]; exists {
					t.Errorf("name-addressed result acquired call_id %v", id)
				}
				if item["name"] != "lookup_fixture" || item["output"] != "synthetic fixture result" {
					t.Errorf("named result changed: %v", item)
				}
			}
			if !found {
				t.Fatal("named result did not reach the upstream")
			}
		})
	}
}
