package dashboard

import (
	"9router/proxy/internal/providers"
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A prefix is the alias every model a node serves is published under. Model
// resolution walks the registry aliases first, so two owners of one prefix is
// not cosmetic: a node claiming `gh` shadows GitHub Copilot entirely and every
// gh/* request answers from the wrong upstream with no error anywhere. Two nodes
// sharing a prefix make routing non-deterministic, the winner depending on map
// iteration order.

func TestHandleCreateProviderNode_RejectsPrefixTakenByRegistryAlias(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
	}{
		{name: "short alias", prefix: "gh"},
		{name: "codebuddy intl alias", prefix: "cbai"},
		{name: "opencode alias", prefix: "oc"},
		{name: "cline alias", prefix: "cl"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, cleanup := setupNodeTestDB(t)
			defer cleanup()
			router := setupTestRouter(repo)

			body := `{"name":"Squatter","prefix":"` + tt.prefix + `","urlSuffix":"squat","baseUrl":"https://squat.example/v1"}`
			req := httptest.NewRequest(http.MethodPost, "/api/provider-nodes", bytes.NewReader([]byte(body)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusConflict {
				t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "PREFIX_CONFLICT") {
				t.Errorf("body = %s, want a PREFIX_CONFLICT code", rec.Body.String())
			}
			// The refusal must not leave a half-written row behind.
			if nodes, err := repo.GetProviderNodes(); err == nil && len(nodes) != 0 {
				t.Errorf("expected no node rows after the refusal, got %d", len(nodes))
			}
		})
	}
}

func TestHandleCreateProviderNode_RejectsPrefixTakenByRegistryID(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// A canonical id with no registry alias is published under itself, so it is
	// an owner too.
	body := `{"name":"Squatter","prefix":"openrouter","urlSuffix":"squat","baseUrl":"https://squat.example/v1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/provider-nodes", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCreateProviderNode_RejectsPrefixTakenByAnotherNode(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	first := `{"name":"First","prefix":"acme","urlSuffix":"acme-1","baseUrl":"https://one.example/v1"}`
	if rec := postNode(t, router, first); rec.Code != http.StatusCreated {
		t.Fatalf("first node: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	second := `{"name":"Second","prefix":"acme","urlSuffix":"acme-2","baseUrl":"https://two.example/v1"}`
	rec := postNode(t, router, second)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second node: expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	// The refusal must name the owner, not just the prefix: an operator needs to
	// know which node to edit.
	if !strings.Contains(rec.Body.String(), "First") {
		t.Errorf("body = %s, want the owning node named", rec.Body.String())
	}
}

func TestHandleCreateProviderNode_PrefixMatchIsCaseInsensitive(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	first := `{"name":"First","prefix":"Acme","urlSuffix":"acme-1","baseUrl":"https://one.example/v1"}`
	if rec := postNode(t, router, first); rec.Code != http.StatusCreated {
		t.Fatalf("first node: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	// A different case is the same prefix on the wire: model ids are matched
	// case-sensitively by clients but resolved through one alias table, so
	// allowing both would let two nodes publish near-identical model ids.
	second := `{"name":"Second","prefix":"ACME","urlSuffix":"acme-2","baseUrl":"https://two.example/v1"}`
	rec := postNode(t, router, second)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second node: expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCreateProviderNode_FreePrefixAccepted(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// A prefix nothing owns must still work: the guard is a refusal, not a
	// lockout.
	body := `{"name":"ACME GW","prefix":"acme-gw","urlSuffix":"acme-gw","baseUrl":"https://acme.example/v1"}`
	if rec := postNode(t, router, body); rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Updating a node must not report its own prefix as a conflict: keeping the
// prefix across an unrelated edit (a base-URL change) is the common case.
func TestHandleUpdateProviderNode_OwnPrefixNotAConflict(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	create := `{"name":"ACME","prefix":"acme","urlSuffix":"acme-1","baseUrl":"https://one.example/v1"}`
	if rec := postNode(t, router, create); rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	update := `{"name":"ACME","prefix":"acme","apiType":"chat","baseUrl":"https://one.example/v2"}`
	req := httptest.NewRequest(http.MethodPut, "/api/provider-nodes/openai-compatible-chat-acme-1", bytes.NewReader([]byte(update)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUpdateProviderNode_RejectsPrefixTakenByAnotherNode(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	if rec := postNode(t, router, `{"name":"First","prefix":"acme","urlSuffix":"acme-1","baseUrl":"https://one.example/v1"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create first: expected 201, got %d", rec.Code)
	}
	if rec := postNode(t, router, `{"name":"Second","prefix":"other","urlSuffix":"other-1","baseUrl":"https://two.example/v1"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create second: expected 201, got %d", rec.Code)
	}

	// Second tries to take First's prefix.
	update := `{"name":"Second","prefix":"acme","baseUrl":"https://two.example/v1"}`
	req := httptest.NewRequest(http.MethodPut, "/api/provider-nodes/openai-compatible-chat-other-1", bytes.NewReader([]byte(update)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

// The helper itself: the guard is only as good as its owner table, so pin what
// it consults.
func TestProviderNodePrefixConflict_OwnerTable(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	h := &DashboardHandler{Repo: repo}

	// Free prefix.
	if got := h.providerNodePrefixConflict("brand-new", ""); got != "" {
		t.Errorf("providerNodePrefixConflict(free) = %q, want empty", got)
	}
	// Registry alias.
	if got := h.providerNodePrefixConflict("gh", ""); !strings.Contains(got, "provider alias") {
		t.Errorf("providerNodePrefixConflict(gh) = %q, want a provider-alias owner", got)
	}
	// Registry id published under itself.
	if got := h.providerNodePrefixConflict("openrouter", ""); !strings.Contains(got, "provider") {
		t.Errorf("providerNodePrefixConflict(openrouter) = %q, want a provider owner", got)
	}
	// Empty prefix is not a conflict — the caller validates presence separately.
	if got := h.providerNodePrefixConflict("  ", ""); got != "" {
		t.Errorf("providerNodePrefixConflict(empty) = %q, want empty", got)
	}
	// Case-insensitive against the registry too.
	if got := h.providerNodePrefixConflict("GH", ""); got == "" {
		t.Error("providerNodePrefixConflict(GH) = empty, want the same owner as gh")
	}

	// A node becomes an owner once it exists, and stops being one when it is
	// the node being edited.
	if rec := postNode(t, router, `{"name":"ACME","prefix":"acme","urlSuffix":"acme-1","baseUrl":"https://one.example/v1"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", rec.Code)
	}
	if got := h.providerNodePrefixConflict("acme", ""); !strings.Contains(got, "ACME") {
		t.Errorf("providerNodePrefixConflict(acme) = %q, want the node named", got)
	}
	if got := h.providerNodePrefixConflict("acme", "openai-compatible-chat-acme-1"); got != "" {
		t.Errorf("providerNodePrefixConflict(acme, self) = %q, want empty", got)
	}
	_ = providers.KnownProviders
}
