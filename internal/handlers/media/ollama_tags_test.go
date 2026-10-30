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

// The family is what an Ollama UI shows beside a model, so a version suffix
// leaking into it reads as a bug.
func TestOllamaFamilyStopsAtTheVersion(t *testing.T) {
	cases := map[string]string{
		"gpt-oss:120b":              "gpt-oss",
		"kimi-k2.5":                 "kimi-k",
		"glm-5":                     "glm",
		"minimax-m2.5":              "minimax-m",
		"glm-4.7-flash":             "glm",
		"qwen3.5":                   "qwen",
		"deepseek-v4.1-flash:cloud": "deepseek-v",
		"clef":                      "clef",
		"clef-flash":                "clef-flash",
	}
	for in, want := range cases {
		if got := ollamaFamily(in); got != want {
			t.Errorf("ollamaFamily(%q) = %q, want %q", in, got, want)
		}
	}
}
