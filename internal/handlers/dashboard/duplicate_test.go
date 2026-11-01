package dashboard

import (
	"testing"

	"9router/proxy/internal/models"
)

func TestIdentityOf_GitHub(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		data     map[string]any
		wantKind string
		wantVal  string
	}{
		{
			name:     "github numeric user id wins",
			provider: "github",
			data: map[string]any{
				"providerSpecificData": map[string]any{"githubUserId": "12345", "githubLogin": "alice"},
			},
			wantKind: "githubUserId",
			wantVal:  "12345",
		},
		{
			name:     "github falls back to login",
			provider: "github",
			data: map[string]any{
				"providerSpecificData": map[string]any{"githubLogin": "alice"},
			},
			wantKind: "githubLogin",
			wantVal:  "alice",
		},
		{
			name:     "antigravity uses google email",
			provider: "antigravity",
			data:     map[string]any{"email": "user@gmail.com"},
			wantKind: "googleEmail",
			wantVal:  "user@gmail.com",
		},
		{
			name:     "codebuddy uses jwt subject",
			provider: "codebuddy-intl",
			data:     map[string]any{"accessToken": "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyLWQifQ.xxx"},
			wantKind: "jwtSubject",
			wantVal:  "user-d",
		},
		{
			name:     "no identity for unknown provider",
			provider: "openai",
			data:     map[string]any{"apiKey": "sk-..."},
			wantKind: "",
			wantVal:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IdentityOf(tt.provider, tt.data)
			if got.Kind != tt.wantKind {
				t.Errorf("IdentityOf() kind = %q, want %q", got.Kind, tt.wantKind)
			}
			if got.Value != tt.wantVal {
				t.Errorf("IdentityOf() value = %q, want %q", got.Value, tt.wantVal)
			}
		})
	}
}

func TestSameIdentity(t *testing.T) {
	a := ConnectionIdentity{Kind: "jwtSubject", Value: "user-1"}
	b := ConnectionIdentity{Kind: "jwtSubject", Value: "user-1"}
	c := ConnectionIdentity{Kind: "jwtSubject", Value: "user-2"}
	d := ConnectionIdentity{Kind: "githubUserId", Value: "user-1"}
	e := ConnectionIdentity{Kind: "", Value: "user-1"}

	if !SameIdentity(a, b) {
		t.Error("same kind and value must match")
	}
	if SameIdentity(a, c) {
		t.Error("different value must not match")
	}
	if SameIdentity(a, d) {
		t.Error("different kind must not match")
	}
	if SameIdentity(a, e) {
		t.Error("empty kind must not match")
	}
}

func TestFindDuplicateConnection(t *testing.T) {
	// Integration test: insert two rows with the same JWT sub and verify
	// findDuplicateConnection returns the first one.
	repo, cleanup := setupDisabledModelsRepo(t)
	defer cleanup()

	conn1 := &models.ProviderConnection{
		ID:       "conn-1",
		Provider: "codebuddy-intl",
		AuthType: "oauth",
		Name:     new("Account A"),
		Data:     `{"accessToken":"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyLWQifQ.xxx"}`,
	}
	conn2 := &models.ProviderConnection{
		ID:       "conn-2",
		Provider: "codebuddy-intl",
		AuthType: "oauth",
		Name:     new("Account B"),
		Data:     `{"accessToken":"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyLWQifQ.yyy"}`,
	}
	if err := repo.CreateProviderConnectionFull(conn1.ID, conn1.Provider, conn1.AuthType, *conn1.Name, nil, conn1.Data); err != nil {
		t.Fatalf("insert conn1: %v", err)
	}
	if err := repo.CreateProviderConnectionFull(conn2.ID, conn2.Provider, conn2.AuthType, *conn2.Name, nil, conn2.Data); err != nil {
		t.Fatalf("insert conn2: %v", err)
	}

	h := &DashboardHandler{Repo: repo}
	incoming := map[string]any{
		"accessToken": "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyLWQifQ.zzz",
	}
	dup, dupData := h.findDuplicateConnection("codebuddy-intl", incoming)
	if dup == nil {
		t.Fatal("expected duplicate, got nil")
	}
	if dup.ID != "conn-1" {
		t.Errorf("duplicate id = %q, want conn-1", dup.ID)
	}
	if dupData == nil {
		t.Fatal("expected dupData, got nil")
	}
}
