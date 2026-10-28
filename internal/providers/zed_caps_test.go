package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The catalogue states far more than the id: the exact windows, the effort
// levels, and whether thinking can be turned off. Losing any of it forces a
// client to guess, which is exactly what this lane exists to avoid.
func TestZedFetchModelsCarriesEveryStatedField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/client/llm_tokens":
			_, _ = w.Write([]byte(`{"token":"h.p.s"}`))
		case "/models":
			_, _ = w.Write([]byte(`{"models":[{
				"provider":"anthropic","id":"claude-sonnet-5-5","display_name":"Claude Sonnet 5.5",
				"is_latest":true,"max_token_count":1000000,"max_token_count_in_max_mode":2000000,
				"max_output_tokens":128000,"supports_tools":true,"supports_images":true,
				"supports_thinking":true,"supports_disabling_thinking":false,
				"supports_parallel_tool_calls":true,"supports_streaming_tools":true,
				"supports_server_side_compaction":true,"supports_max_mode":true,"supports_fast_mode":true,
				"is_disabled":false,
				"supported_effort_levels":[{"name":"Low","value":"low"},{"name":"High","value":"high","is_default":true},{"name":"Max","value":"max"}]
			}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	previous := ZedCloudBaseURL()
	SetZedCloudBaseURL(srv.URL)
	defer SetZedCloudBaseURL(previous)

	models, err := ZedFetchModels(context.Background(), srv.Client(), ZedCredentials{
		AccessToken: "blob", UserID: "493663", OrganizationID: "org_1",
	})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("models = %d, want 1", len(models))
	}
	m := models[0]
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"MaxTokenCount", m.MaxTokenCount, 1000000},
		{"MaxOutputTokens", m.MaxOutputTokens, 128000},
		{"SupportsImages", m.SupportsImages, true},
		{"SupportsDisablingThinking", m.SupportsDisablingThinking, false},
		{"SupportsParallelToolCalls", m.SupportsParallelToolCalls, true},
		{"SupportsStreamingTools", m.SupportsStreamingTools, true},
		{"SupportsServerSideCompaction", m.SupportsServerSideCompaction, true},
		{"SupportsMaxMode", m.SupportsMaxMode, true},
		{"SupportsFastMode", m.SupportsFastMode, true},
		{"EffortDefault", m.EffortDefault, "high"},
		{"MaxTokenCountInMaxMode", *m.MaxTokenCountInMaxMode, 2000000},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if len(m.EffortLevels) != 3 || m.EffortLevels[0] != "low" || m.EffortLevels[2] != "max" {
		t.Errorf("EffortLevels = %v, want [low high max]", m.EffortLevels)
	}
}
