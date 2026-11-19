package dashboard

import (
	"context"
	"net/http"
	"testing"
)

// The dashboard's Qoder model list must advertise each confirmed key under
// its sanitized display_name as well, so importing the catalog offers
// `qfmodel` and `qwen3.8-flash` as one model's two ids — the same ids chat
// resolves through the executor's catalog.

func stubQoderProbe(t *testing.T, body string) {
	t.Helper()
	orig := validateProbeDo
	validateProbeDo = func(_ context.Context, _, _ string, _ map[string]string, _ []byte) (int, []byte, error) {
		return http.StatusOK, []byte(body), nil
	}
	t.Cleanup(func() { validateProbeDo = orig })
}

func qoderModelIDs(models []QoderModel) []string {
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestFetchQoderCatalogModelsAdvertisesDisplayNames(t *testing.T) {
	stubQoderProbe(t, `{"chat":[
		{"key":"qfmodel","enable":true,"display_name":"Qwen3.8-Flash","max_input_tokens":131072,"max_output_tokens":8192},
		{"key":"dmodel","enable":true,"display_name":"Deep Seek  V4  Pro"},
		{"key":"auto","enable":true,"display_name":"Auto"},
		{"key":"hidden","enable":false,"display_name":"Hidden Model"}
	]}`)

	models, err := fetchQoderCatalogModels(context.Background(), "qoder", "dt-token", map[string]any{"userId": "u1"})
	if err != nil {
		t.Fatalf("fetchQoderCatalogModels: %v", err)
	}

	want := []string{"qfmodel", "dmodel", "auto", "deep-seek-v4-pro", "qwen3.8-flash"}
	got := qoderModelIDs(models)
	if len(got) != len(want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ids[%d] = %q, want %q (full list %v)", i, got[i], want[i], got)
		}
	}

	// The friendly id carries the same limits and human-facing name as its key.
	for _, m := range models {
		if m.ID == "qwen3.8-flash" {
			if m.ContextLength != 131072 || m.MaxOutputTokens != 8192 {
				t.Errorf("friendly id lost the key's limits: %+v", m)
			}
			if m.Name != "Qwen3.8-Flash" {
				t.Errorf("friendly id name = %q, want the display name", m.Name)
			}
		}
	}
}

// A display name that is another model's live key must not be advertised
// twice, and two keys sharing a name resolve deterministically in raw-key
// order. Unusable names advertise only the key.
func TestFetchQoderCatalogModelsNameCollisions(t *testing.T) {
	stubQoderProbe(t, `{"chat":[
		{"key":"qfmodel","enable":true,"display_name":"gfmodel"},
		{"key":"gfmodel","enable":true},
		{"key":"kmodel_latest","enable":true,"display_name":"Kimi K2"},
		{"key":"kmodel","enable":true,"display_name":"Kimi-K2"},
		{"key":"mmodel","enable":true,"display_name":"MiniMax M3!"}
	]}`)

	models, err := fetchQoderCatalogModels(context.Background(), "qoder", "dt-token", map[string]any{"userId": "u1"})
	if err != nil {
		t.Fatalf("fetchQoderCatalogModels: %v", err)
	}

	want := []string{"qfmodel", "gfmodel", "kmodel_latest", "kmodel", "mmodel", "kimi-k2"}
	got := qoderModelIDs(models)
	if len(got) != len(want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ids[%d] = %q, want %q (full list %v)", i, got[i], want[i], got)
		}
	}
}
