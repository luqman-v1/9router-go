package dashboard

import (
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeNavVisibility(t *testing.T) {
	longList := make([]any, 0, 40)
	for i := range 40 {
		longList = append(longList, fmt.Sprintf("tab-%02d", i))
	}
	longOrder := make([]any, 0, maxNavOrderEntries+6)
	for i := range maxNavOrderEntries + 6 {
		longOrder = append(longOrder, fmt.Sprintf("tab-%02d", i))
	}

	tests := []struct {
		name           string
		input          any
		want           []string
		wantCount      int
		wantOrder      []string
		wantOrderCount int
		wantNoOrder    bool
		clear          bool
	}{
		{
			name:  "no value at all clears the setting",
			input: nil,
			clear: true,
		},
		{
			name:  "a scalar instead of an object clears the setting",
			input: "quota",
			clear: true,
		},
		{
			name:  "an object without a hidden list clears the setting",
			input: map[string]any{"visible": []any{"quota"}},
			clear: true,
		},
		{
			name:  "a scalar hidden clears the setting",
			input: map[string]any{"hidden": "quota"},
			clear: true,
		},
		{
			name:  "an empty list stores an empty list",
			input: map[string]any{"hidden": []any{}},
			want:  []string{},
		},
		{
			name: "ids are lower-cased, trimmed and de-duplicated",
			input: map[string]any{"hidden": []any{
				"Quota", " quota ", "media-tts", "MEDIA-TTS",
			}},
			want: []string{"quota", "media-tts"},
		},
		{
			name: "settings can never be hidden",
			input: map[string]any{"hidden": []any{
				"settings", "security", "Settings",
			}},
			want: []string{"security"},
		},
		{
			name:  "ids that cannot address a tab are dropped",
			input: map[string]any{"hidden": []any{"bad id", "../etc", "", "quota"}},
			want:  []string{"quota"},
		},
		{
			name:      "the list is capped",
			input:     map[string]any{"hidden": longList},
			wantCount: maxHiddenNavEntries,
		},
		{
			name:        "an absent arrangement keeps only the hide list",
			input:       map[string]any{"hidden": []any{"quota"}},
			want:        []string{"quota"},
			wantNoOrder: true,
		},
		{
			name: "the arrangement keeps the order it arrived in, settings included",
			input: map[string]any{
				"hidden": []any{},
				"order":  []any{"plans", "settings", "quota"},
			},
			want:      []string{},
			wantOrder: []string{"plans", "settings", "quota"},
		},
		{
			name: "the arrangement is lower-cased, trimmed and de-duplicated",
			input: map[string]any{
				"hidden": []any{},
				"order":  []any{"Plans", " plans ", " quota ", "bad id"},
			},
			want:      []string{},
			wantOrder: []string{"plans", "quota"},
		},
		{
			name:  "a scalar arrangement clears the setting rather than the hide list alone",
			input: map[string]any{"hidden": []any{"quota"}, "order": "plans"},
			clear: true,
		},
		{
			name:           "the arrangement is capped",
			input:          map[string]any{"hidden": []any{}, "order": longOrder},
			want:           []string{},
			wantOrderCount: maxNavOrderEntries,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeNavVisibility(tt.input)
			if tt.clear {
				if got != nil {
					t.Fatalf("normalizeNavVisibility() = %v, want nil (clear)", got)
				}
				return
			}
			if got == nil {
				t.Fatal("normalizeNavVisibility() = nil, want a stored shape")
			}
			hidden, ok := got["hidden"].([]string)
			if !ok {
				t.Fatalf("normalizeNavVisibility() hidden = %#v, want []string", got["hidden"])
			}
			if tt.wantCount > 0 {
				if len(hidden) != tt.wantCount {
					t.Fatalf("hidden has %d entries, want %d", len(hidden), tt.wantCount)
				}
				if hidden[0] != "tab-00" || hidden[1] != "tab-01" {
					t.Errorf("capping must keep the first ids, got %v", hidden[:2])
				}
				return
			}
			if strings.Join(hidden, ",") != strings.Join(tt.want, ",") {
				t.Errorf("hidden = %v, want %v", hidden, tt.want)
			}
			order, hasOrder := got["order"].([]string)
			switch {
			case tt.wantOrder != nil:
				if !hasOrder {
					t.Fatalf("order = %#v, want %v", got["order"], tt.wantOrder)
				}
				if strings.Join(order, ",") != strings.Join(tt.wantOrder, ",") {
					t.Errorf("order = %v, want %v", order, tt.wantOrder)
				}
			case tt.wantOrderCount > 0:
				if len(order) != tt.wantOrderCount {
					t.Fatalf("order has %d entries, want %d", len(order), tt.wantOrderCount)
				}
				if order[0] != "tab-00" || order[1] != "tab-01" {
					t.Errorf("capping must keep the first ids, got %v", order[:2])
				}
			case tt.wantNoOrder:
				if _, present := got["order"]; present {
					t.Errorf("order = %#v, want it absent", got["order"])
				}
			}
		})
	}
}

func TestHandleUpdateSettingsNavVisibility(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	put := func(t *testing.T, payload string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT /api/settings = %d: %s", rec.Code, rec.Body.String())
		}
		return rec
	}

	getSettings := func(t *testing.T) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/settings = %d: %s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode settings: %v", err)
		}
		return body
	}

	// A junk entry alongside a real one: the sidebar must only ever see ids it
	// can address, and the always-visible Settings entry has to survive.
	put(t, `{"navVisibility":{"hidden":["quota","SETTINGS","odd id","quota","media-tts"]}}`)
	body := getSettings(t)
	visibility, ok := body["navVisibility"].(map[string]any)
	if !ok {
		t.Fatalf("navVisibility missing from GET /api/settings: %#v", body["navVisibility"])
	}
	hidden, _ := visibility["hidden"].([]any)
	if len(hidden) != 2 || hidden[0] != "quota" || hidden[1] != "media-tts" {
		t.Fatalf("hidden = %v, want [quota media-tts]", hidden)
	}

	// Restoring every entry has to clear the stored key, not keep hiding them.
	put(t, `{"navVisibility":{"hidden":[]}}`)
	body = getSettings(t)
	visibility, ok = body["navVisibility"].(map[string]any)
	if !ok {
		t.Fatalf("empty list must round-trip, got %#v", body["navVisibility"])
	}
	if hidden, _ := visibility["hidden"].([]any); len(hidden) != 0 {
		t.Fatalf("hidden = %v, want an empty list", hidden)
	}

	// The arrangement round-trips beside the hide list. `settings` may sit in it
	// — the operator is free to move the entry, only the hide list refuses it.
	put(t, `{"navVisibility":{"hidden":["quota"],"order":["plans","settings","quota"]}}`)
	body = getSettings(t)
	visibility, ok = body["navVisibility"].(map[string]any)
	if !ok {
		t.Fatalf("an arrangement must round-trip, got %#v", body["navVisibility"])
	}
	order, _ := visibility["order"].([]any)
	if len(order) != 3 || order[0] != "plans" || order[1] != "settings" || order[2] != "quota" {
		t.Fatalf("order = %v, want [plans settings quota]", order)
	}
	if hidden, _ := visibility["hidden"].([]any); len(hidden) != 1 || hidden[0] != "quota" {
		t.Fatalf("hidden = %v, want [quota]", hidden)
	}

	// Re-hide, then clear with an unparsable payload: the previous list must not
	// linger, otherwise the panel keeps hiding entries the operator restored.
	put(t, `{"navVisibility":{"hidden":["quota"]}}`)
	put(t, `{"navVisibility":{"hidden":"quota"}}`)
	body = getSettings(t)
	if _, leaked := body["navVisibility"]; leaked {
		t.Fatalf("an unparsable payload must clear the setting, got %#v", body["navVisibility"])
	}
}
