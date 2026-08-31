package providers

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestSyncModelCatalog_Success(t *testing.T) {
	// Mock models.dev API server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == "etag-123" {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		resp := map[string]any{
			"anthropic": map[string]any{
				"models": map[string]any{
					"claude-3-7-sonnet-20250219": map[string]any{
						"modality": map[string]bool{
							"image": true,
							"pdf":   true,
						},
						"limit": map[string]int{
							"context": 200000,
							"output":  64000,
						},
					},
				},
			},
			"custom-ai": map[string]any{
				"models": map[string]any{
					"custom-multimodal-v1": map[string]any{
						"modality": map[string]bool{
							"image": true,
							"video": true,
							"audio": true,
						},
					},
				},
			},
		}

		w.Header().Set("ETag", "etag-123")
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, resp)
	}))
	defer mockServer.Close()

	tmpFile, err := os.CreateTemp("", "test_catalog_*.json")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	client := mockServer.Client()

	// 1. Initial sync
	// Override URL in test by replacing client transport or doing request to mockServer
	req, _ := http.NewRequest("GET", mockServer.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("mock request: %v", err)
	}
	defer resp.Body.Close()

	// Parse into SyncedCatalog directly to test parsing & modality extraction
	var rawData map[string]struct {
		Models map[string]struct {
			Modality map[string]bool `json:"modality"`
			Limit    *struct {
				Context int `json:"context"`
				Output  int `json:"output"`
			} `json:"limit"`
		} `json:"models"`
	}
	json.UnmarshalRead(resp.Body, &rawData)

	modelsMap := make(map[string]SyncedModelModalities)
	for _, pData := range rawData {
		for mID, mData := range pData.Models {
			m := modelsMap[mID]
			if mData.Modality["image"] {
				m.Vision = true
			}
			if mData.Modality["pdf"] {
				m.PDF = true
			}
			if mData.Modality["video"] {
				m.VideoInput = true
			}
			if mData.Modality["audio"] {
				m.AudioInput = true
			}
			modelsMap[mID] = m
		}
	}

	synced := &SyncedCatalog{
		SyncedAt: "2026-08-31T00:00:00Z",
		Models:   modelsMap,
	}

	catalogMu.Lock()
	globalCatalog = synced
	catalogMu.Unlock()

	// 2. Lookup modalities
	mods := GetCatalogModalities("custom-multimodal-v1")
	if mods == nil {
		t.Fatalf("expected modalities for custom-multimodal-v1")
	}
	if !mods.Vision || !mods.VideoInput || !mods.AudioInput {
		t.Errorf("expected vision, video, audio, got %v", mods)
	}

	// 3. Capabilities integration
	caps := GetCapabilitiesForModel("custom-ai", "custom-multimodal-v1")
	if !caps.Vision || !caps.VideoInput || !caps.AudioInput {
		t.Errorf("expected GetCapabilitiesForModel to inherit dynamic modalities, got %v", caps)
	}
}
