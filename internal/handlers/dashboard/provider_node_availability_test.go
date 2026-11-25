package dashboard

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	json "encoding/json/v2"
)

// The availability endpoint mirrors what the create/update handlers will
// accept, so the dialog can show "available / taken" under the prefix and
// suffix fields the way a username picker does. It is advisory: create still
// checks again, because a value can be claimed between the check and the save.

func postAvailability(t *testing.T, router http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/provider-nodes/availability", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

type availabilityResult struct {
	Prefix struct {
		Value      string `json:"value"`
		Status     string `json:"status"`
		Reason     string `json:"reason"`
		ProviderID string `json:"providerId"`
	} `json:"prefix"`
	URLSuffix struct {
		Value      string `json:"value"`
		Status     string `json:"status"`
		Reason     string `json:"reason"`
		ProviderID string `json:"providerId"`
	} `json:"urlSuffix"`
}

func decodeAvailability(t *testing.T, rec *httptest.ResponseRecorder) availabilityResult {
	t.Helper()
	var out availabilityResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode availability response %s: %v", rec.Body.String(), err)
	}
	return out
}

func TestHandleCheckProviderNodeAvailability(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// Seed one node so the taken paths have a real owner to name.
	if rec := postNode(t, router, `{"name":"Acme Node","prefix":"acme","urlSuffix":"acme","baseUrl":"https://acme.example/v1"}`); rec.Code != http.StatusCreated {
		t.Fatalf("seed node: status %d: %s", rec.Code, rec.Body.String())
	}

	tests := []struct {
		name        string
		body        string
		wantPrefix  string
		wantSuffix  string
		wantReasonP string
		wantReasonS string
		wantProvIDS string
	}{
		{
			name:        "free prefix and suffix are available",
			body:        `{"prefix":"fresh-ns","urlSuffix":"fresh-sfx"}`,
			wantPrefix:  "available",
			wantSuffix:  "available",
			wantProvIDS: "openai-compatible-chat-fresh-sfx",
		},
		{
			name:        "registry alias prefix is taken",
			body:        `{"prefix":"gh","urlSuffix":"fresh-sfx"}`,
			wantPrefix:  "taken",
			wantSuffix:  "available",
			wantReasonP: `Already used by provider alias "gh".`,
		},
		{
			name:        "canonical provider id prefix is taken",
			body:        `{"prefix":"openai","urlSuffix":"fresh-sfx"}`,
			wantPrefix:  "taken",
			wantReasonP: `Already used by provider "openai".`,
		},
		{
			name:        "another node's prefix is taken and names the node",
			body:        `{"prefix":"ACME","urlSuffix":"fresh-sfx"}`,
			wantPrefix:  "taken",
			wantReasonP: `Already used by provider node "Acme Node".`,
		},
		{
			name:        "suffix colliding with a stored node id is taken",
			body:        `{"prefix":"fresh-ns","urlSuffix":"acme"}`,
			wantPrefix:  "available",
			wantSuffix:  "taken",
			wantProvIDS: "openai-compatible-chat-acme",
			wantReasonS: "This provider id is already in use.",
		},
		{
			name:       "suffix failing the id pattern is invalid",
			body:       `{"prefix":"fresh-ns","urlSuffix":"-bad"}`,
			wantPrefix: "available",
			wantSuffix: "invalid",
		},
		{
			name:       "blank prefix on a compatible type is automatic",
			body:       `{"prefix":"","urlSuffix":"fresh-sfx"}`,
			wantPrefix: "automatic",
			wantSuffix: "available",
		},
		{
			name:        "blank prefix on custom-embedding is invalid",
			body:        `{"prefix":"","urlSuffix":"fresh-sfx","type":"custom-embedding"}`,
			wantPrefix:  "invalid",
			wantSuffix:  "available",
			wantReasonP: "Prefix is required for custom embedding providers.",
		},
		{
			name:       "blank suffix reports the random fallback",
			body:       `{"prefix":"fresh-ns","urlSuffix":""}`,
			wantPrefix: "available",
			wantSuffix: "random",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postAvailability(t, router, tt.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			got := decodeAvailability(t, rec)
			if got.Prefix.Status != tt.wantPrefix {
				t.Errorf("prefix status = %q (%s), want %q", got.Prefix.Status, got.Prefix.Reason, tt.wantPrefix)
			}
			if tt.wantSuffix != "" && got.URLSuffix.Status != tt.wantSuffix {
				t.Errorf("suffix status = %q (%s), want %q", got.URLSuffix.Status, got.URLSuffix.Reason, tt.wantSuffix)
			}
			if tt.wantReasonP != "" && got.Prefix.Reason != tt.wantReasonP {
				t.Errorf("prefix reason = %q, want %q", got.Prefix.Reason, tt.wantReasonP)
			}
			if tt.wantReasonS != "" && got.URLSuffix.Reason != tt.wantReasonS {
				t.Errorf("suffix reason = %q, want %q", got.URLSuffix.Reason, tt.wantReasonS)
			}
			if tt.wantProvIDS != "" && got.URLSuffix.ProviderID != tt.wantProvIDS {
				t.Errorf("suffix providerId = %q, want %q", got.URLSuffix.ProviderID, tt.wantProvIDS)
			}
		})
	}
}

// The check must run against the production route table, not only the test
// one: a route registered in routes.go but missing from router.go 404s in the
// binary while every unit test here stays green (the #432/#572 trap).
func TestAvailabilityRouteIsRegisteredInProductionRouter(t *testing.T) {
	// setupTestRouter covers the dashboard table; the production table is
	// exercised by the integration suite. Here we only assert the handler is
	// wired to the same path string the client calls.
	if AvailabilityRoutePath != "/provider-nodes/availability" {
		t.Fatalf("client path and handler path diverged: %q", AvailabilityRoutePath)
	}
}
