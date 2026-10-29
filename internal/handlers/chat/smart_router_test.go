package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

// upstream f3f86fdd parity: tests/unit/combo-smart-router.test.js covers
// state extraction, the choice answer shape, ordering, and the policies.

func TestExtractSmartPromptState(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"latest user turn wins", `{"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"hi"},{"role":"user","content":"latest question"}]}`, "latest question"},
		{"array content parts join", `{"messages":[{"role":"user","content":[{"type":"text","text":"part one"},{"type":"image_url"},{"type":"text","text":"part two"}]}]}`, "part one part two"},
		{"no user role falls back to last message", `{"messages":[{"role":"system","content":"sys"},{"role":"assistant","content":"answer"}]}`, "answer"},
		{"responses input array", `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"resp prompt"}]}]}`, "resp prompt"},
		{"string prompt", `{"prompt":"plain prompt"}`, "plain prompt"},
		{"string input", `{"input":"string input"}`, "string input"},
		{"empty body", `{}`, ""},
		{"invalid json", `not json`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractSmartPromptState([]byte(tt.body)); got != tt.want {
				t.Errorf("extractSmartPromptState() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("state is truncated to the classifier cap", func(t *testing.T) {
		long := strings.Repeat("x", smartMaxStateChars+100)
		got := extractSmartPromptState([]byte(`{"prompt":"` + long + `"}`))
		if len(got) != smartMaxStateChars {
			t.Errorf("truncated length = %d, want %d", len(got), smartMaxStateChars)
		}
	})
}

func TestParseSmartRouteAnswer(t *testing.T) {
	t.Run("full answer", func(t *testing.T) {
		choice := parseSmartRouteAnswer([]byte(`{"answers":{"route":{"choice":"a/b","confidence":0.9,"probabilities":{"a/b":0.9,"c/d":0.1}}}}`))
		if choice == nil || choice.Choice != "a/b" {
			t.Fatalf("expected choice a/b, got %+v", choice)
		}
		if choice.Confidence == nil || *choice.Confidence != 0.9 {
			t.Errorf("confidence = %v", choice.Confidence)
		}
		if choice.Probabilities["c/d"] != 0.1 {
			t.Errorf("probabilities = %v", choice.Probabilities)
		}
	})
	t.Run("answer without confidence still decides", func(t *testing.T) {
		choice := parseSmartRouteAnswer([]byte(`{"answers":{"route":{"choice":"a/b"}}}`))
		if choice == nil || choice.Choice != "a/b" || choice.Confidence != nil {
			t.Fatalf("unexpected choice %+v", choice)
		}
	})
	t.Run("missing or empty choice is no decision", func(t *testing.T) {
		for _, body := range []string{`{}`, `{"answers":{}}`, `{"answers":{"route":{}}}`, `{"answers":{"route":{"choice":"  "}}}`, `garbage`} {
			if parseSmartRouteAnswer([]byte(body)) != nil {
				t.Errorf("%q parsed as a decision", body)
			}
		}
	})
}

func TestOrderModelsBySmartChoice(t *testing.T) {
	models := []string{"m1", "m2", "m3"}
	t.Run("chosen model first, rest by probability", func(t *testing.T) {
		got := orderModelsBySmartChoice(models, &smartChoice{Choice: "m3", Probabilities: map[string]float64{"m1": 0.2, "m2": 0.7}})
		want := []string{"m3", "m2", "m1"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("no probabilities keeps original order", func(t *testing.T) {
		got := orderModelsBySmartChoice(models, &smartChoice{Choice: "m1"})
		if strings.Join(got, ",") != "m1,m2,m3" {
			t.Errorf("got %v", got)
		}
	})
	t.Run("choice outside the combo changes nothing", func(t *testing.T) {
		got := orderModelsBySmartChoice(models, &smartChoice{Choice: "other"})
		if strings.Join(got, ",") != "m1,m2,m3" {
			t.Errorf("got %v", got)
		}
	})
	t.Run("input order is never mutated", func(t *testing.T) {
		orderModelsBySmartChoice(models, &smartChoice{Choice: "m2", Probabilities: map[string]float64{"m1": 1}})
		if strings.Join(models, ",") != "m1,m2,m3" {
			t.Errorf("original slice mutated: %v", models)
		}
	})
}

func TestSmartSwitchThreshold(t *testing.T) {
	custom := func(v float64) *float64 { return &v }
	sticky := true
	tests := []struct {
		name string
		cfg  db.ComboStrategy
		want float64
	}{
		{"defaults", db.ComboStrategy{}, 0.8},
		{"explicit smart keeps its threshold", db.ComboStrategy{SmartPolicy: "smart", ConfidenceThreshold: custom(0.75)}, 0.75},
		{"smart-switch prefix keeps its threshold", db.ComboStrategy{SmartPolicy: "smart-switch-85", ConfidenceThreshold: custom(0.85)}, 0.85},
		{"smart-switch-70 pins 0.7", db.ComboStrategy{SmartPolicy: "smart-switch-70"}, 0.7},
		{"smart-switch-90 pins 0.9", db.ComboStrategy{SmartPolicy: "smart-switch-90"}, 0.9},
		{"custom uses its threshold", db.ComboStrategy{SmartPolicy: "custom", ConfidenceThreshold: custom(0.66)}, 0.66},
		{"custom without threshold falls back", db.ComboStrategy{SmartPolicy: "custom"}, 0.8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = sticky
			if got := smartSwitchThreshold(tt.cfg); got != tt.want {
				t.Errorf("smartSwitchThreshold() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSmartStickyStore(t *testing.T) {
	h := &ChatHandler{}
	models := []string{"m1", "m2"}

	t.Run("set and get round-trips", func(t *testing.T) {
		h.setSmartStickyModel("combo", "s1", "m1")
		if got := h.getSmartStickyModel("combo", "s1", models); got != "m1" {
			t.Fatalf("got %q, want m1", got)
		}
	})

	t.Run("a model that left the combo clears the pin", func(t *testing.T) {
		if got := h.getSmartStickyModel("combo", "s1", []string{"m2"}); got != "" {
			t.Fatalf("expected cleared pin, got %q", got)
		}
		if got := h.getSmartStickyModel("combo", "s1", models); got != "" {
			t.Fatalf("pin survived the membership loss, got %q", got)
		}
	})

	t.Run("expired entry is dropped", func(t *testing.T) {
		h.setSmartStickyModel("combo", "s2", "m2")
		h.smartStickyMu.Lock()
		h.smartSticky["combo:s2"] = smartStickyEntry{Model: "m2", LastUsed: time.Now().Add(-smartStickyTTL - time.Minute)}
		h.smartStickyMu.Unlock()
		if got := h.getSmartStickyModel("combo", "s2", models); got != "" {
			t.Fatalf("expired pin returned %q", got)
		}
	})

	t.Run("clear one session keeps the others", func(t *testing.T) {
		h.setSmartStickyModel("combo", "a", "m1")
		h.setSmartStickyModel("combo", "b", "m2")
		h.clearSmartStickySession("combo", "a")
		if h.getSmartStickyModel("combo", "a", models) != "" {
			t.Error("session a survived its clear")
		}
		if h.getSmartStickyModel("combo", "b", models) != "m2" {
			t.Error("session b was cleared with a")
		}
	})

	t.Run("prune drops the oldest quarter at the ceiling", func(t *testing.T) {
		full := &ChatHandler{}
		full.smartSticky = make(map[string]smartStickyEntry)
		for i := 0; i < smartMaxStickySessions; i++ {
			key := smartStickyKey("c", strings.Repeat("s", 10)+time.Duration(i).String())
			full.smartSticky[key] = smartStickyEntry{Model: "m1", LastUsed: time.Now().Add(-time.Duration(smartMaxStickySessions-i) * time.Minute)}
		}
		full.pruneSmartSticky()
		full.smartStickyMu.Lock()
		size := len(full.smartSticky)
		full.smartStickyMu.Unlock()
		if size >= smartMaxStickySessions {
			t.Fatalf("prune left %d entries at the ceiling", size)
		}
	})
}

// newSmartRouterHandler wires a handler whose default router provider points
// at the fake System One endpoint, with no connection needed (NoAuth).
func newSmartRouterHandler(t *testing.T, upstream *httptest.Server) *ChatHandler {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)

	previous, known := providers.KnownProviders["opencode"]
	overridden := providers.ProviderConfig{BaseURL: upstream.URL + "/v1/chat/completions", AuthHeader: "Authorization", AuthScheme: "bearer", NoAuth: true, SystemoneURL: upstream.URL + "/v1/systemone"}
	providers.KnownProviders["opencode"] = overridden
	t.Cleanup(func() {
		if known {
			providers.KnownProviders["opencode"] = previous
		} else {
			delete(providers.KnownProviders, "opencode")
		}
	})
	return NewChatHandler(db.NewRepo(database))
}

func TestResolveSmartChoice(t *testing.T) {
	t.Run("orders by the upstream decision", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"answers":{"route":{"choice":"p/deepseek","confidence":0.95,"probabilities":{"p/deepseek":0.95,"p/glm":0.05}}}}`))
		}))
		defer upstream.Close()
		h := newSmartRouterHandler(t, upstream)

		choice := h.resolveSmartChoice(context.Background(),
			[]byte(`{"messages":[{"role":"user","content":"write sql"}]}`),
			[]string{"p/glm", "p/deepseek"}, "")
		if choice == nil || choice.Choice != "p/deepseek" {
			t.Fatalf("unexpected choice %+v", choice)
		}
	})

	t.Run("fails open on upstream error", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer upstream.Close()
		h := newSmartRouterHandler(t, upstream)
		if choice := h.resolveSmartChoice(context.Background(), []byte(`{"prompt":"x"}`), []string{"a", "b"}, ""); choice != nil {
			t.Fatalf("expected fail-open nil, got %+v", choice)
		}
	})

	t.Run("empty prompt state skips the evaluation", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("upstream must not be reached with no prompt state")
		}))
		defer upstream.Close()
		h := newSmartRouterHandler(t, upstream)
		if choice := h.resolveSmartChoice(context.Background(), []byte(`{}`), []string{"a", "b"}, ""); choice != nil {
			t.Fatalf("expected nil, got %+v", choice)
		}
	})

	t.Run("single-model combo never evaluates", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("upstream must not be reached for a one-model combo")
		}))
		defer upstream.Close()
		h := newSmartRouterHandler(t, upstream)
		if choice := h.resolveSmartChoice(context.Background(), []byte(`{"prompt":"x"}`), []string{"only"}, ""); choice != nil {
			t.Fatalf("expected nil, got %+v", choice)
		}
	})
}

func TestApplySmartRouting(t *testing.T) {
	models := []string{"p/glm", "p/deepseek", "p/kimi"}
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)

	t.Run("no decision keeps the combo order and carries no state", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"answers":{}}`))
		}))
		defer upstream.Close()
		h := newSmartRouterHandler(t, upstream)

		ctx, ordered := h.applySmartRouting(context.Background(), body, models, "combo-x")
		if strings.Join(ordered, ",") != strings.Join(models, ",") {
			t.Errorf("order changed without a decision: %v", ordered)
		}
		if ctx.Value(smartRouteStateKey{}) != nil {
			t.Error("routing state attached without a decision")
		}
	})

	t.Run("a decision above the floor pins and orders", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"answers":{"route":{"choice":"p/deepseek","confidence":0.9,"probabilities":{"p/kimi":0.08,"p/glm":0.02}}}}`))
		}))
		defer upstream.Close()
		h := newSmartRouterHandler(t, upstream)
		ctx := handlerutil.WithSessionID(context.Background(), "sess-1")

		_, ordered := h.applySmartRouting(ctx, body, models, "combo-y")
		if strings.Join(ordered, ",") != "p/deepseek,p/kimi,p/glm" {
			t.Errorf("got %v", ordered)
		}
		if got := h.getSmartStickyModel("combo-y", "sess-1", models); got != "p/deepseek" {
			t.Errorf("session pin = %q, want p/deepseek", got)
		}
		_ = ctx
	})

	t.Run("a decision below the floor keeps the combo order", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"answers":{"route":{"choice":"p/kimi","confidence":0.3}}}`))
		}))
		defer upstream.Close()
		h := newSmartRouterHandler(t, upstream)

		_, ordered := h.applySmartRouting(context.Background(), body, models, "combo-z")
		if strings.Join(ordered, ",") != strings.Join(models, ",") {
			t.Errorf("low-confidence decision reordered the combo: %v", ordered)
		}
	})

	t.Run("strict policy reuses the pin without evaluating", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("strict lock must not re-evaluate")
		}))
		defer upstream.Close()
		h := newSmartRouterHandler(t, upstream)
		h.setSmartStickyModel("combo-strict", "s9", "p/kimi")
		// The saved policy is read from settings; without a row the default is
		// smart, so strict is exercised through the pin plus a policy write.
		if err := h.Repo.UpdateSettingsRaw(map[string]any{"comboStrategies": map[string]any{
			"combo-strict": map[string]any{"smartPolicy": "strict"},
		}}); err != nil {
			t.Fatalf("write settings: %v", err)
		}
		ctx := handlerutil.WithSessionID(context.Background(), "s9")

		_, ordered := h.applySmartRouting(ctx, body, models, "combo-strict")
		if ordered[0] != "p/kimi" {
			t.Errorf("strict lock did not pin first: %v", ordered)
		}
	})

	t.Run("per-turn never pins", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"answers":{"route":{"choice":"p/glm","confidence":0.95}}}`))
		}))
		defer upstream.Close()
		h := newSmartRouterHandler(t, upstream)
		if err := h.Repo.UpdateSettingsRaw(map[string]any{"comboStrategies": map[string]any{
			"combo-turn": map[string]any{"smartPolicy": "per-turn"},
		}}); err != nil {
			t.Fatalf("write settings: %v", err)
		}
		ctx := handlerutil.WithSessionID(context.Background(), "s10")

		_, ordered := h.applySmartRouting(ctx, body, models, "combo-turn")
		if ordered[0] != "p/glm" {
			t.Errorf("per-turn ignored the decision: %v", ordered)
		}
		if h.getSmartStickyModel("combo-turn", "s10", models) != "" {
			t.Error("per-turn pinned a session")
		}
	})
}

func TestClearFailedSmartSticky(t *testing.T) {
	h := &ChatHandler{}
	h.setSmartStickyModel("combo", "s1", "p/glm")
	ctx := withSmartRouteState(context.Background(), &smartRouteState{comboName: "combo", sessionID: "s1", activeModel: "p/glm"})

	h.clearFailedSmartSticky(ctx, "p/other")
	if h.getSmartStickyModel("combo", "s1", []string{"p/glm"}) != "p/glm" {
		t.Fatal("unrelated failure cleared the pin")
	}
	h.clearFailedSmartSticky(ctx, "p/glm")
	if h.getSmartStickyModel("combo", "s1", []string{"p/glm"}) != "" {
		t.Fatal("pinned-model failure did not clear the pin")
	}
}
