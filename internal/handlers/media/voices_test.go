package media

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleAudioVoices_EdgeTTS(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"ShortName":"en-US-AriaNeural","FriendlyName":"Microsoft Aria Online (Natural) - English (United States)","Locale":"en-US","Gender":"Female"},
			{"ShortName":"en-US-GuyNeural","FriendlyName":"Microsoft Guy Online (Natural) - English (United States)","Locale":"en-US","Gender":"Male"},
			{"ShortName":"fr-FR-DeniseNeural","FriendlyName":"Microsoft Denise Online (Natural) - French (France)","Locale":"fr-FR","Gender":"Female"}
		]`))
	}))
	defer upstream.Close()

	origURL := edgeVoicesURL
	edgeVoicesURL = upstream.URL + "/list"
	defer func() { edgeVoicesURL = origURL }()

	handler := NewMediaHandler(nil, nil, nil)
	req := httptest.NewRequest("GET", "/audio/voices?lang=en", nil)
	rec := httptest.NewRecorder()
	handler.HandleAudioVoices(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Voices    []map[string]any `json:"voices"`
		Languages []map[string]any `json:"languages"`
		ByLang    map[string]any   `json:"byLang"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}

	if len(resp.Voices) != 2 {
		t.Fatalf("expected 2 voices after lang=en filter, got %d: %v", len(resp.Voices), resp.Voices)
	}
	v := resp.Voices[0]
	if v["name"] != "Aria (English (United States)" {
		t.Errorf("name normalization wrong: %q", v["name"])
	}
	if v["langName"] != "English" || v["countryName"] != "United States" {
		t.Errorf("names wrong: langName=%v countryName=%v", v["langName"], v["countryName"])
	}

	if len(resp.Languages) != 1 || resp.Languages[0]["code"] != "en" {
		t.Errorf("expected single en language group, got %v", resp.Languages)
	}
	en, ok := resp.ByLang["en"].(map[string]any)
	if !ok {
		t.Fatalf("expected en group in byLang, got %v", resp.ByLang)
	}
	if got := len(en["voices"].([]any)); got != 2 {
		t.Errorf("expected 2 voices in en group, got %d", got)
	}
}
