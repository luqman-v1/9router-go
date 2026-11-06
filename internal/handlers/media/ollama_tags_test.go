package media

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An Ollama client lists models from its base URL before it will talk to it.
// Without /api/tags the gateway looks like an Ollama server with nothing
// installed, so `ollama list` prints an empty table and no model is offered.
func TestHandleOllamaTagsListsTheRoutableModels(t *testing.T) {
	h := &MediaHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/tags", nil)
	rec := httptest.NewRecorder()

	h.HandleOllamaTags(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// Ollama clients are often browser-based and send a preflight; a response
	// without CORS fails on a healthy gateway.
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}

	var resp struct {
		Models []struct {
			Name    string `json:"name"`
			Model   string `json:"model"`
			Details struct {
				Family string `json:"family"`
				Format string `json:"format"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(resp.Models) == 0 {
		t.Fatal("no models listed")
	}
	// name and model both have to be set: Ollama clients read `model` on some
	// paths and `name` on others.
	for _, m := range resp.Models {
		if m.Name == "" || m.Model == "" {
			t.Errorf("entry %+v has an empty name or model", m)
		}
		if m.Details.Format != "gguf" {
			t.Errorf("%s: format = %q, want gguf", m.Name, m.Details.Format)
		}
	}
}

// The family is what an Ollama UI shows beside a model. Ollama reports versioned
// families (qwen3, gemma3), so only registry decoration is stripped: a leading
// provider path and a trailing :tag.
func TestOllamaFamilyStripsOnlyRegistryDecoration(t *testing.T) {
	cases := map[string]string{
		"gpt-oss:120b":              "gpt-oss",
		"kimi-k2.5":                 "kimi-k2.5",
		"glm-5":                     "glm-5",
		"minimax-m2.5":              "minimax-m2.5",
		"glm-4.7-flash":             "glm-4.7-flash",
		"qwen3.5":                   "qwen3.5",
		"deepseek-v4.1-flash:cloud": "deepseek-v4.1-flash",
		"clef":                      "clef",
		"clef-flash":                "clef-flash",
		"":                          "",
	}
	for in, want := range cases {
		if got := ollamaFamily(in); got != want {
			t.Errorf("ollamaFamily(%q) = %q, want %q", in, got, want)
		}
	}
}
