package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The dashboard lets an operator skip the prefix on an anthropic/openai
// compatible node, and fills it for them rather than leaving it blank: a blank
// prefix would publish the node's models under its own id — 31 to 59
// characters in front of every model id — which is the length problem the
// optional field exists to avoid.
//
// The fill runs once, on write, and is stored like any other prefix. It is
// never re-derived on read: a prefix that changed under a stored combo would
// orphan every "<prefix>/<model>" that names this node.
//
// custom-embedding keeps the requirement — its catalog is addressed as
// `<prefix>/<model>` everywhere and a blank prefix would make two embedding
// nodes indistinguishable.

// shortEnoughForAModelID is the property this fill exists for. The two
// fallbacks it replaces are far longer: a generated uuid tail is 36
// characters, and the composed node id is 31 to 59.
func shortEnoughForAModelID(t *testing.T, prefix string) {
	t.Helper()
	if len(prefix) > maxDerivedPrefixLength {
		t.Errorf("derived prefix %q is %d chars, want at most %d", prefix, len(prefix), maxDerivedPrefixLength)
	}
}

func TestHandleCreateProviderNode_BlankPrefixFilledFromURLSuffix(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantCode int
		// wantPrefix is checked only for the accepted cases.
		wantPrefix string
		// nodeID is set for accepted cases so the stored row can be read back.
		nodeID string
	}{
		{
			// The suffix is the one short, meaningful string the operator
			// already chose for this node, so it is what the prefix becomes.
			name:       "openai-compatible uses the typed suffix",
			body:       `{"name":"No Prefix OA","prefix":"","urlSuffix":"np-oa","baseUrl":"https://oa.example/v1"}`,
			wantCode:   http.StatusCreated,
			wantPrefix: "np-oa",
			nodeID:     "openai-compatible-chat-np-oa",
		},
		{
			name:       "anthropic-compatible uses the typed suffix",
			body:       `{"name":"No Prefix AC","prefix":"","type":"anthropic-compatible","urlSuffix":"np-ac","baseUrl":"https://ac.example/v1"}`,
			wantCode:   http.StatusCreated,
			wantPrefix: "np-ac",
			nodeID:     "anthropic-compatible-np-ac",
		},
		{
			// Embeddings are listed as `<prefix>/<model>` by the media views, so
			// a blank one would leave nothing to address the catalog by.
			name:     "custom-embedding still requires a prefix",
			body:     `{"name":"No Prefix EM","prefix":"","type":"custom-embedding","urlSuffix":"np-em","baseUrl":"https://em.example/v1"}`,
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, cleanup := setupNodeTestDB(t)
			defer cleanup()
			router := setupTestRouter(repo)

			rec := postNode(t, router, tt.body)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantCode, rec.Body.String())
			}
			if tt.wantCode != http.StatusCreated {
				if !strings.Contains(rec.Body.String(), "prefix is required") {
					t.Errorf("body = %s, want the prefix requirement named", rec.Body.String())
				}
				return
			}
			_, data, err := repo.GetProviderNodeByID(tt.nodeID)
			if err != nil || data == nil {
				t.Fatalf("GetProviderNodeByID(%q): data=%v err=%v", tt.nodeID, data, err)
			}
			if data.Prefix != tt.wantPrefix {
				t.Errorf("stored prefix = %q, want %q", data.Prefix, tt.wantPrefix)
			}
			// The response has to carry the same value the row holds, or the
			// dashboard renders a prefix the gateway will never serve.
			if !strings.Contains(rec.Body.String(), `"prefix":"`+tt.wantPrefix+`"`) {
				t.Errorf("response body = %s, want prefix %q", rec.Body.String(), tt.wantPrefix)
			}
		})
	}
}

// A generated suffix is a uuid — 36 characters, longer than the node-id
// fallback the fill exists to replace — so the token draw takes over. What
// must never happen is storing the uuid, or the composed node id, as the
// prefix.
func TestHandleCreateProviderNode_GeneratedSuffixYieldsShortToken(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// No urlSuffix: the id tail is a generated uuid.
	rec := postNode(t, router, `{"name":"Token Node","prefix":"","baseUrl":"https://tok.example/v1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var id string
	if err := repo.RawDB().QueryRow(`SELECT id FROM providerNodes WHERE name = ?`, "Token Node").Scan(&id); err != nil {
		t.Fatalf("read node id: %v", err)
	}
	_, data, err := repo.GetProviderNodeByID(id)
	if err != nil || data == nil {
		t.Fatalf("GetProviderNodeByID(%q): data=%v err=%v", id, data, err)
	}
	if data.Prefix == "" {
		t.Fatal("stored prefix is blank, want a generated token")
	}
	shortEnoughForAModelID(t, data.Prefix)
	if data.Prefix == id {
		t.Errorf("prefix %q is the node id, want a short token", data.Prefix)
	}
	if len(data.Prefix) < 8 {
		t.Errorf("prefix %q is %d chars, want the full 8-char token", data.Prefix, len(data.Prefix))
	}
}

// A suffix is unique per composed node id, not per prefix: two node types may
// legally share one, and a suffix may spell a registry alias. Claiming either
// as a prefix would shadow a real provider — exactly what
// providerNodePrefixConflict exists to prevent — so the token draw takes over
// instead of answering 409 on a value the operator never typed as a prefix.
func TestFillBlankPrefix_SkipsSuffixThatWouldShadow(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	h := &DashboardHandler{Repo: repo}

	tests := []struct {
		name   string
		suffix string
		// seedPrefix is another node's stored prefix, when the case is about
		// two nodes sharing a suffix.
		seedPrefix string
		wantSuffix bool
	}{
		{name: "free suffix is used", suffix: "prod", wantSuffix: true},
		{name: "registry alias suffix is skipped", suffix: "gh"},
		{name: "registry id suffix is skipped", suffix: "openrouter"},
		{name: "another node's prefix is skipped", suffix: "taken", seedPrefix: "taken"},
		// The suffix pattern allows 64 characters; a prefix that long would be
		// worse than the node id it replaces.
		{name: "overlong suffix is skipped", suffix: strings.Repeat("a", 65)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.seedPrefix != "" {
				if _, err := repo.CreateProviderNode("openai-compatible-chat-other", "openai-compatible", "Other",
					`{"prefix":"`+tt.seedPrefix+`","apiType":"chat","baseUrl":"https://other.example/v1"}`); err != nil {
					t.Fatalf("seed node: %v", err)
				}
			}
			got := h.fillBlankPrefix("", tt.suffix, "")
			if got == "" {
				t.Fatal("fillBlankPrefix returned blank, want a token fallback")
			}
			shortEnoughForAModelID(t, got)
			if tt.wantSuffix && got != tt.suffix {
				t.Errorf("fillBlankPrefix(%q) = %q, want the suffix", tt.suffix, got)
			}
			if !tt.wantSuffix && got == tt.suffix {
				t.Errorf("fillBlankPrefix(%q) = the suffix, want it skipped", tt.suffix)
			}
		})
	}
}

// A prefix the operator typed is theirs: the fill must pass it through without
// consulting the suffix or drawing a token, or an edit would silently rename
// every model id the node has already published.
func TestFillBlankPrefix_TypedPrefixIsNeverRewritten(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	h := &DashboardHandler{Repo: repo}

	for _, typed := range []string{"acme", "gh", strings.Repeat("x", 65)} {
		if got := h.fillBlankPrefix(typed, "prod", ""); got != typed {
			t.Errorf("fillBlankPrefix(%q, ...) = %q, want it untouched", typed, got)
		}
	}
}

// A node stored with a blank prefix must not answer a blank lookup. The fill
// makes such rows rare, but rows written before it existed still exist, and
// without the guard GetProviderNodeByPrefix("") returns whichever prefixless
// row the scan reaches first.
func TestGetProviderNodeByPrefix_BlankNeverMatches(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()

	if _, err := repo.CreateProviderNode("openai-compatible-chat-np-oa", "openai-compatible", "No Prefix OA",
		`{"prefix":"","apiType":"chat","baseUrl":"https://oa.example/v1"}`); err != nil {
		t.Fatalf("CreateProviderNode: %v", err)
	}

	node, data, err := repo.GetProviderNodeByPrefix("")
	if err != nil {
		t.Fatalf("GetProviderNodeByPrefix: %v", err)
	}
	if node != nil || data != nil {
		t.Errorf("GetProviderNodeByPrefix(\"\") = %v, want no match for a blank prefix", node)
	}
	// The node is still reachable by its own id, which is the namespace its
	// models publish under until it is edited.
	if node, _, err := repo.GetProviderNodeByID("openai-compatible-chat-np-oa"); err != nil || node == nil {
		t.Errorf("GetProviderNodeByID: node=%v err=%v, want the blank-prefix node reachable by id", node, err)
	}
}

// Clearing the prefix on an edit is not a way to get a blank one back: the
// same fill runs, so the node keeps a short namespace instead of falling back
// to its 31-to-59-character id. excludeID keeps the node's own stored prefix
// from reading as a collision with itself.
func TestHandleUpdateProviderNode_BlankPrefixRefilled(t *testing.T) {
	repo, cleanup := setupNodeTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	create := `{"name":"ACME","prefix":"acme","urlSuffix":"acme-1","baseUrl":"https://one.example/v1"}`
	if rec := postNode(t, router, create); rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	update := `{"name":"ACME","prefix":"","apiType":"chat","baseUrl":"https://one.example/v2"}`
	req := httptest.NewRequest(http.MethodPut, "/api/provider-nodes/openai-compatible-chat-acme-1", strings.NewReader(update))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	_, data, err := repo.GetProviderNodeByID("openai-compatible-chat-acme-1")
	if err != nil || data == nil {
		t.Fatalf("GetProviderNodeByID: data=%v err=%v", data, err)
	}
	// The suffix was "acme-1", so that is what the cleared prefix becomes —
	// not a token, and certainly not a blank that would publish under the id.
	if data.Prefix != "acme-1" {
		t.Errorf("stored prefix = %q, want the suffix %q", data.Prefix, "acme-1")
	}
	shortEnoughForAModelID(t, data.Prefix)
}
