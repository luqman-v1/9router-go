package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
)

func TestVisionAdapter_E2E_ChatCompletions_AutoSwitch(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	var agCalled, dsCalled atomic.Int32

	// Mock upstream for Antigravity (Gemini native response)
	agUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agCalled.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"I see a red square"}],"role":"model"}}]}`))
	}))
	defer agUpstream.Close()

	// Mock upstream for DeepSeek (Text-only model)
	dsUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dsCalled.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"model does not support images","type":"invalid_request_error"}}`))
	}))
	defer dsUpstream.Close()

	// Configure settings with Capacity Adapter pointing to ag/gemini-3.8-flash-high
	settingsJSON, _ := json.Marshal(map[string]any{
		"capacityAdapter": map[string]any{
			"vision": map[string]any{
				"enabled": true,
				"models":  []string{"ag/gemini-3.8-flash-high"},
			},
		},
	})
	_, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, string(settingsJSON))
	if err != nil {
		t.Fatalf("failed to update settings: %v", err)
	}

	// Insert mock connections for deepseek and antigravity
	dsData, _ := json.Marshal(map[string]any{
		"apiKey":  "sk-test-deepseek-key",
		"baseUrl": dsUpstream.URL,
	})
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-ds', 'deepseek', 'apikey', 'Mock DeepSeek', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(dsData))
	if err != nil {
		t.Fatalf("failed to insert deepseek connection: %v", err)
	}

	agData, _ := json.Marshal(map[string]any{
		"apiKey":      "sk-test-ag-key",
		"accessToken": "sk-test-ag-key",
		"baseUrl":     agUpstream.URL,
		"projectId":   "mock-project-123",
		"expiresAt":   "2099-01-01T00:00:00Z",
	})
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-ag', 'antigravity', 'apikey', 'Mock Antigravity', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(agData))
	if err != nil {
		t.Fatalf("failed to insert antigravity connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// Send an OpenAI chat completions request targeting deepseek/deepseek-chat with an image input
	reqBody := `{
		"model": "deepseek/deepseek-chat",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "What is in this image?"},
					{"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="}}
				]
			}
		],
		"stream": false
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d, body: %s", rec.Code, rec.Body.String())
	}

	if agCalled.Load() != 1 {
		t.Errorf("expected Antigravity vision model to be called 1 time, got %d", agCalled.Load())
	}

	if dsCalled.Load() != 0 {
		t.Errorf("expected DeepSeek text-only model not to be called, got %d", dsCalled.Load())
	}

	respBody := rec.Body.String()
	if !strings.Contains(respBody, "I see a red square") {
		t.Errorf("expected vision response, got: %s", respBody)
	}
}

func TestVisionAdapter_E2E_Messages_AutoSwitch(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	var agCalled atomic.Int32

	// Mock upstream for Antigravity (handles translated OpenAI request)
	agUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agCalled.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"Claude says image recognized"}],"role":"model"}}]}`))
	}))
	defer agUpstream.Close()

	settingsJSON, _ := json.Marshal(map[string]any{
		"capacityAdapter": map[string]any{
			"vision": map[string]any{
				"enabled": true,
				"models":  []string{"ag/gemini-3.8-flash-high"},
			},
		},
	})
	_, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, string(settingsJSON))
	if err != nil {
		t.Fatalf("failed to update settings: %v", err)
	}

	agData, _ := json.Marshal(map[string]any{
		"apiKey":      "sk-test-ag-key",
		"accessToken": "sk-test-ag-key",
		"baseUrl":     agUpstream.URL,
		"projectId":   "mock-project-123",
		"expiresAt":   "2099-01-01T00:00:00Z",
	})
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-ag-msg', 'antigravity', 'apikey', 'Mock Antigravity', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(agData))
	if err != nil {
		t.Fatalf("failed to insert antigravity connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// Send an Anthropic /v1/messages request with image block to deepseek/deepseek-chat
	claudeBody := `{
		"model": "deepseek/deepseek-chat",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "Describe image"},
					{
						"type": "image",
						"source": {
							"type": "base64",
							"media_type": "image/png",
							"data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
						}
					}
				]
			}
		],
		"max_tokens": 100,
		"stream": false
	}`

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(claudeBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleMessages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d, body: %s", rec.Code, rec.Body.String())
	}

	if agCalled.Load() != 1 {
		t.Errorf("expected Antigravity vision model to be called 1 time, got %d", agCalled.Load())
	}

	respBody := rec.Body.String()
	if !strings.Contains(respBody, "Claude says image recognized") {
		t.Errorf("expected vision response in translated format, got: %s", respBody)
	}
}
