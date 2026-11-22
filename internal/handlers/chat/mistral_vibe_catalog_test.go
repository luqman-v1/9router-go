package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/models"
)

func TestAppendConnectionModelsMistralVibeCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"mistral-large-4","capabilities":{"completion_chat":true}},
			{"id":"mistral-embed","capabilities":{"completion_chat":false}},
			{"id":"mistral-large","capabilities":{"completion_chat":true}}
		]}`))
	}))
	defer srv.Close()

	dataJSON, err := json.Marshal(map[string]any{
		"apiKey":  "vibe-test-key",
		"baseUrl": srv.URL,
		"providerSpecificData": map[string]any{
			"usageLane": "vibe",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	conn := &models.ProviderConnection{ID: "vibe-catalog", Provider: "mistral", Data: string(dataJSON)}
	h := &ChatHandler{Client: srv.Client()}
	got := h.appendConnectionModels(context.Background(), nil, map[string]bool{}, conn, "mistral",
		nil, nil, map[string]string{}, func(string, string) bool { return false })

	ids := make([]string, 0, len(got))
	for _, model := range got {
		ids = append(ids, model.ID)
	}
	joined := strings.Join(ids, ",")
	for _, want := range []string{"mistral/mistral-large-4", "mistral/mistral-large"} {
		if !strings.Contains(joined, want) {
			t.Errorf("vibe catalog missing %s: %v", want, ids)
		}
	}
	if strings.Contains(joined, "mistral-embed") {
		t.Errorf("vibe catalog must not contain non-chat models: %v", ids)
	}
	aliasCount := 0
	for _, id := range ids {
		if id == "mistral/mistral-large" {
			aliasCount++
		}
	}
	if aliasCount != 1 {
		t.Errorf("gateway alias should appear exactly once, got %d: %v", aliasCount, ids)
	}
}

func TestParseModelsFeedCompletionChatCapability(t *testing.T) {
	models := parseModelsFeedLiveModels([]byte(`{"data":[
		{"id":"chat","capabilities":{"completion_chat":true}},
		{"id":"embed","capabilities":{"completion_chat":false}},
		{"id":"unknown"}
	]}`))
	if len(models) != 3 {
		t.Fatalf("parsed %d models, want 3", len(models))
	}
	if models[0].CompletionChat == nil || !*models[0].CompletionChat {
		t.Error("chat capability was not parsed as true")
	}
	if models[1].CompletionChat == nil || *models[1].CompletionChat {
		t.Error("non-chat capability was not parsed as false")
	}
	if models[2].CompletionChat != nil {
		t.Error("missing capability should stay nil")
	}
}

func TestAPILaneKeepsMistralCatalogUnchanged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"mistral-embed","capabilities":{"completion_chat":false}}]}`))
	}))
	defer srv.Close()

	dataJSON, err := json.Marshal(map[string]any{
		"apiKey":  "api-lane-key",
		"baseUrl": srv.URL,
		"providerSpecificData": map[string]any{
			"usageLane": "api",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	conn := &models.ProviderConnection{ID: "api-catalog", Provider: "mistral", Data: string(dataJSON)}
	h := &ChatHandler{Client: srv.Client()}
	got := h.appendConnectionModels(context.Background(), nil, map[string]bool{}, conn, "mistral",
		nil, nil, map[string]string{}, func(string, string) bool { return false })
	for _, model := range got {
		if strings.Contains(model.ID, "mistral-large") {
			t.Errorf("API lane must not gain Vibe aliases: %v", got)
		}
	}
}
