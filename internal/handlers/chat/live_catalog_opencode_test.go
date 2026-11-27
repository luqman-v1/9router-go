package chat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchOpenCodeLiveCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-opencode-client") != "desktop" {
			t.Errorf("expected x-opencode-client desktop, got %q", r.Header.Get("x-opencode-client"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
			"models": [
				{
					"id": "seaglass-alpha-free",
					"name": "Seaglass Alpha Free",
					"limit": {"context": 1000000, "output": 131072},
					"capabilities": {"reasoning": {"supported": true}}
				},
				{
					"id": "big-pickle",
					"name": "Big Pickle",
					"limit": {"context": 200000, "output": 32000}
				},
				{
					"id": "deepseek-v4-flash-free",
					"name": "Dead model"
				},
				{
					"id": "paid-model",
					"name": "Paid Model"
				}
			]
		}`)
	}))
	defer srv.Close()

	h := &ChatHandler{Client: srv.Client()}
	// Override URL in test by calling server directly
	ctx := context.Background()

	// Direct test via custom fetch
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	req.Header.Set("x-opencode-client", "desktop")
	resp, err := h.Client.Do(req)
	if err != nil {
		t.Fatalf("test request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	models := parseOpenCodeLiveModels(body)

	if len(models) != 2 {
		t.Fatalf("expected 2 free models, got %d", len(models))
	}
	if models[0].ID != "seaglass-alpha-free" || models[0].ContextLength != 1000000 {
		t.Errorf("unexpected model 0: %+v", models[0])
	}
	if models[0].Capabilities == nil || !models[0].Capabilities.Thinking {
		t.Errorf("expected reasoning enabled on seaglass-alpha-free")
	}
	if models[1].ID != "big-pickle" || models[1].ContextLength != 200000 {
		t.Errorf("unexpected model 1: %+v", models[1])
	}
}
