package chat

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

// TestDispatch_PrefetchesRemoteImagesForBase64OnlyTargets is the G11 wiring
// contract: a remote image URL in the request must be inlined before an
// upstream that cannot fetch URLs itself sees it, and must be left alone for
// one that can.
func TestDispatch_PrefetchesRemoteImagesForBase64OnlyTargets(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 120))
	imageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer imageSrv.Close()

	prev := imagePrefetchOpts
	imagePrefetchOpts = translator.PrefetchOptions{AllowLoopback: true, Timeout: 5e9}
	t.Cleanup(func() { imagePrefetchOpts = prev })

	body := []byte(`{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"` + imageSrv.URL + `/a.png"}}]}]}`)

	t.Run("InlinedForGemini", func(t *testing.T) {
		out := prefetchImagesForTarget(t.Context(), "gemini", "m", nil, body)
		if strings.Contains(string(out), imageSrv.URL) {
			t.Fatalf("remote URL survived the prefetch for a base64-only target:\n%s", out)
		}
		if !strings.Contains(string(out), "data:image/png;base64,") {
			t.Fatalf("no inline data URL in the rewritten body:\n%s", out)
		}
	})

	t.Run("UntouchedForOpenAI", func(t *testing.T) {
		out := prefetchImagesForTarget(t.Context(), "openai", "m", nil, body)
		if string(out) != string(body) {
			t.Fatalf("an OpenAI-compatible target fetches URLs itself; body was rewritten:\n%s", out)
		}
	})
}

// TestDispatch_DispatchesThroughPrefetch pins that the wiring is actually
// reached by a real request, not merely available: the same body that a
// base64-only provider receives must arrive at the upstream with the image
// already inlined.
func TestDispatch_DispatchesThroughPrefetch(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 120))
	imageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer imageSrv.Close()

	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	prev := imagePrefetchOpts
	imagePrefetchOpts = translator.PrefetchOptions{AllowLoopback: true, Timeout: 5e9}
	t.Cleanup(func() { imagePrefetchOpts = prev })

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	seedConnDB(t, database, "ollama", "conn-prefetch", "sk-test", upstream.URL)
	handler := NewChatHandler(db.NewRepo(database))

	reqBody := `{"model":"ollama/m","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"what is this"},` +
		`{"type":"image_url","image_url":{"url":"` + imageSrv.URL + `/a.png"}}]}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(gotBody, imageSrv.URL) {
		t.Fatalf("the upstream received the remote URL; the dispatch skipped the prefetch:\n%s", gotBody)
	}
	if !strings.Contains(gotBody, "data:image/png;base64,") {
		t.Fatalf("the upstream received no inline image:\n%s", gotBody)
	}
}

// TestWantsInlineImages pins the target set, including the config-driven case
// that has no registry name of its own.
func TestWantsInlineImages(t *testing.T) {
	for _, p := range []string{"gemini", "gemini-cli", "vertex", "antigravity", "ollama", "kiro", "commandcode"} {
		if !wantsInlineImages(p, "", nil) {
			t.Errorf("%s needs inline images but was not recognised", p)
		}
	}
	for _, p := range []string{"openai", "claude", "deepseek", "openrouter"} {
		if wantsInlineImages(p, "", &providers.ProviderConfig{}) {
			t.Errorf("%s fetches remote URLs itself; prefetching would cost a download for nothing", p)
		}
	}
	if !wantsInlineImages("some-self-hosted", "", &providers.ProviderConfig{Format: "gemini-native"}) {
		t.Error("a gemini-native provider must be recognised even when its id is unknown")
	}
}

// TestWantsInlineImages_FollowsTheTargetFormat pins the property the allowlist
// actually needs: the wire format the request will be translated INTO decides
// whether an image has to be inlined, not the provider's name. Upstream keys
// TARGETS_NEED_BASE64 by format for this reason; a Go allowlist keyed only by
// provider id misses a provider whose id is unknown but whose model rides the
// Gemini lane — opencode-go's gemini-4-argon-contributor is translated by
// TranslateOpenAIToGemini, which turns a remote URL into FileData.fileUri, a
// reference Gemini cannot fetch.
func TestWantsInlineImages_FollowsTheTargetFormat(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		cfg      *providers.ProviderConfig
		want     bool
	}{
		{
			name:     "opencode-go gemini model rides the gemini lane",
			provider: "opencode-go",
			model:    "gemini-4-argon-contributor",
			want:     true,
		},
		{
			name:     "opencode-go uncatalogued gemini id still resolves to gemini",
			provider: "opencode-go",
			model:    "gemini-9-preview",
			want:     true,
		},
		{
			name:     "opencode-go chat model keeps its remote URL",
			provider: "opencode-go",
			model:    "step-5-preview",
			want:     false,
		},
		{
			name:     "openai-compatible target keeps its remote URL",
			provider: "openrouter",
			model:    "some/model",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wantsInlineImages(tt.provider, tt.model, tt.cfg); got != tt.want {
				t.Errorf("wantsInlineImages(%q, %q) = %v, want %v", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}
