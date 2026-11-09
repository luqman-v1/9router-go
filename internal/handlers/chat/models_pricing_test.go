package chat

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/pricing"
)

func TestHandleModels_Pricing(t *testing.T) {
	handler := NewChatHandler(nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	handler.HandleModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Data []ModelInfoObject `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Data) == 0 {
		t.Fatal("expected models in /v1/models response, got 0")
	}

	var foundFree, foundPaid bool
	for _, m := range resp.Data {
		if m.Pricing == nil {
			continue
		}
		if pricing.IsFreeModel(m.ID) {
			foundFree = true
			if m.Pricing.Prompt != "0" || m.Pricing.Completion != "0" || m.Pricing.Input != 0 || m.Pricing.Output != 0 {
				t.Errorf("free model %s pricing = %+v, want prompt 0, completion 0, input 0, output 0", m.ID, m.Pricing)
			}
		} else if m.Pricing.Input > 0 {
			foundPaid = true
			if m.Pricing.Prompt == "" || m.Pricing.Prompt == "0" {
				t.Errorf("paid model %s has empty or zero prompt string: %q", m.ID, m.Pricing.Prompt)
			}
			if m.Pricing.Completion == "" || m.Pricing.Completion == "0" {
				t.Errorf("paid model %s has empty or zero completion string: %q", m.ID, m.Pricing.Completion)
			}
		}
	}

	if !foundFree {
		t.Log("no free model encountered in default static models, ok")
	}
	if !foundPaid {
		t.Error("expected to find at least one paid model with pricing in /v1/models")
	}
}
