package providers

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestNousCatalogNormalization(t *testing.T) {
	raw := []map[string]any{
		{
			"id":             "vendor/text-model",
			"name":           "Vendor Text Model",
			"context_length": 262144,
			"top_provider": map[string]any{
				"max_completion_tokens": 65536,
			},
			"architecture": map[string]any{
				"output_modalities": []any{"text"},
			},
		},
		{
			"id": "vendor/embedding-model",
			"architecture": map[string]any{
				"output_modalities": []any{"embeddings"},
			},
		},
		{
			"id": "vendor/image-model",
			"architecture": map[string]any{
				"output_modalities": []any{"image"},
			},
		},
		{
			"id":             "nousresearch/first-party-model",
			"name":           "Nous First Party",
			"description":    "First party reasoning model",
			"context_length": 262144,
			"architecture": map[string]any{
				"output_modalities": []any{"text"},
			},
		},
	}

	models := NormalizeNousModels(raw)
	if len(models) != 2 {
		t.Fatalf("expected 2 normalized models, got %d", len(models))
	}

	// First-party model must sort before third-party
	if models[0].ID != "nousresearch/first-party-model" {
		t.Errorf("expected first-party model first, got %q", models[0].ID)
	}
	if models[1].ID != "vendor/text-model" {
		t.Errorf("expected vendor model second, got %q", models[1].ID)
	}

	// Verify text model context and max output tokens
	textModel := models[1]
	if textModel.ContextLength == nil || *textModel.ContextLength != 262144 {
		t.Errorf("expected contextLength 262144, got %v", textModel.ContextLength)
	}
	if textModel.MaxOutputTokens == nil || *textModel.MaxOutputTokens != 65536 {
		t.Errorf("expected maxOutputTokens 65536, got %v", textModel.MaxOutputTokens)
	}
}

func TestNousCapabilities(t *testing.T) {
	nousModels := GetProviderModels("nous")
	if len(nousModels) == 0 {
		t.Fatal("no models registered for provider nous")
	}

	// Exact values from the verified table in
	// docs/superpowers/specs/2026-10-19-nous-provider-spec.md §3.1, not
	// thresholds. The earlier form asserted `ContextWindow >= 262144`, which
	// passes for any larger value — so a later edit could move longcat-2.0 from
	// its documented 1048756 to 262144, quietly shrinking the biggest window in
	// the catalogue, and this test would stay green. §7.1 calls the acceptance
	// "khai báo chính xác ... context window, max output tokens", and a bound is
	// not an exact declaration.
	want := map[string]struct {
		contextWindow int
		maxOutput     int
		vision, video bool
	}{
		"meituan/longcat-2.0:free":         {1048756, 262144, false, false},
		"meituan/longcat-2.5-preview:free": {1048576, 131072, true, false},
		"stepfun/step-5-preview:free":      {1000000, 64000, true, true},
		"upstage/solar-mini4:free":         {524288, 131072, false, false},
		"stepfun/step-3.7-flash:free":      {262144, 256000, true, true},
		"poolside/laguna-s-2.1:free":       {262144, 131072, false, false},
		"poolside/laguna-xs-2.1:free":      {262144, 32768, false, false},
		"stealth/missingno":                {262144, 131072, false, false},
	}

	// A model added to the registry without a row here would pass no assertion
	// at all, which is the same vacuous-pass failure §5.D.6 names.
	if len(nousModels) != len(want) {
		t.Fatalf("registry has %d nous models, spec §3.1 declares %d", len(nousModels), len(want))
	}

	for _, id := range nousModels {
		t.Run(id, func(t *testing.T) {
			w, ok := want[id]
			if !ok {
				t.Fatalf("%s is registered but not in the spec's §3.1 table", id)
			}
			caps := GetCapabilitiesForModel("nous", id)
			if !caps.Tools {
				t.Errorf("%s: expected tools=true", id)
			}
			if !caps.Reasoning {
				t.Errorf("%s: expected reasoning=true", id)
			}
			if caps.ThinkingFormat != "nous" {
				t.Errorf("%s: expected thinkingFormat=nous, got %q", id, caps.ThinkingFormat)
			}
			if caps.ContextWindow != w.contextWindow {
				t.Errorf("%s: expected contextWindow=%d (spec §3.1), got %d", id, w.contextWindow, caps.ContextWindow)
			}
			if caps.MaxOutput != w.maxOutput {
				t.Errorf("%s: expected maxOutput=%d (spec §3.1), got %d", id, w.maxOutput, caps.MaxOutput)
			}
			if caps.Vision != w.vision {
				t.Errorf("%s: expected vision=%v, got %v", id, w.vision, caps.Vision)
			}
			if caps.VideoInput != w.video {
				t.Errorf("%s: expected videoInput=%v, got %v", id, w.video, caps.VideoInput)
			}
		})
	}
}

func TestNousAttribution(t *testing.T) {
	t.Run("default attribution tags added", func(t *testing.T) {
		body := []byte(`{"model":"stepfun/step-5-preview:free","messages":[{"role":"user","content":"hi"}]}`)
		out := ApplyNousAttribution(body)

		var req map[string]any
		if err := json.Unmarshal(out, &req); err != nil {
			t.Fatalf("failed to unmarshal output: %v", err)
		}

		rawTags, ok := req["tags"].([]any)
		if !ok {
			t.Fatalf("expected tags array in output, got %T: %s", req["tags"], out)
		}

		var tags []string
		for _, tag := range rawTags {
			if s, ok := tag.(string); ok {
				tags = append(tags, s)
			}
		}

		want := []string{"product=9router", "client=9router", "user=9router"}
		for _, w := range want {
			if !slices.Contains(tags, w) {
				t.Errorf("missing tag %q in %v", w, tags)
			}
		}
	})

	t.Run("preserves caller product tag and removes empty tags", func(t *testing.T) {
		body := []byte(`{"model":"stepfun/step-5-preview:free","messages":[{"role":"user","content":"hi"}],"tags":["product=my-app","user=","client= ","custom=value"]}`)
		out := ApplyNousAttribution(body)

		var req map[string]any
		if err := json.Unmarshal(out, &req); err != nil {
			t.Fatalf("failed to unmarshal output: %v", err)
		}

		rawTags, ok := req["tags"].([]any)
		if !ok {
			t.Fatalf("expected tags array in output, got %T: %s", req["tags"], out)
		}

		var tags []string
		for _, tag := range rawTags {
			if s, ok := tag.(string); ok {
				tags = append(tags, s)
			}
		}

		// product=my-app must be preserved, not overwritten with product=9router
		if !slices.Contains(tags, "product=my-app") {
			t.Errorf("expected caller product=my-app preserved, got %v", tags)
		}
		if slices.Contains(tags, "product=9router") {
			t.Errorf("product=9router should not overwrite product=my-app: %v", tags)
		}
		if !slices.Contains(tags, "custom=value") {
			t.Errorf("expected custom=value preserved: %v", tags)
		}
		// Empty user= and client= should be cleaned up and filled with 9router defaults
		if !slices.Contains(tags, "client=9router") {
			t.Errorf("expected client=9router in %v", tags)
		}
		if !slices.Contains(tags, "user=9router") {
			t.Errorf("expected user=9router in %v", tags)
		}
		if slices.Contains(tags, "user=") || slices.Contains(tags, "client= ") {
			t.Errorf("empty tags not removed: %v", tags)
		}
	})

	t.Run("preserves malformed caller tags for upstream validation", func(t *testing.T) {
		for _, raw := range []string{
			`{"model":"stepfun/step-5-preview:free","messages":[{"role":"user","content":"hi"}],"tags":"user=alice"}`,
			`{"model":"stepfun/step-5-preview:free","messages":[{"role":"user","content":"hi"}],"tags":42}`,
			`{"model":"stepfun/step-5-preview:free","messages":[{"role":"user","content":"hi"}],"tags":{"user":"alice"}}`,
		} {
			out := ApplyNousAttribution([]byte(raw))
			if string(out) != raw {
				t.Errorf("expected malformed tags preserved unchanged: got %s, want %s", string(out), raw)
			}
		}
	})
}

func TestShouldApplyNousAttribution(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{url: "https://api.example.test/v1", want: false},
		{url: "https://inference-api.nousresearch.com.evil.test/v1", want: false},
		{url: "https://user@evil.test/v1", want: false},
		{url: "http://inference-api.nousresearch.com/v1", want: false},
		{url: "not-a-url", want: false},
		{url: "https://inference-api.nousresearch.com/v1", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := ShouldApplyNousAttribution(tt.url)
			if got != tt.want {
				t.Errorf("ShouldApplyNousAttribution(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestApplyNousThinking(t *testing.T) {
	t.Run("reasoning level set to high", func(t *testing.T) {
		body := []byte(`{"model":"meituan/longcat-2.0:free","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)
		out := ApplyNousThinking(body, "nous")

		var req map[string]any
		if err := json.Unmarshal(out, &req); err != nil {
			t.Fatalf("failed to unmarshal output: %v", err)
		}
		if _, exists := req["reasoning_effort"]; exists {
			t.Errorf("expected reasoning_effort removed, but was present in %s", out)
		}
		reasoning, ok := req["reasoning"].(map[string]any)
		if !ok {
			t.Fatalf("expected nested reasoning object in %s", out)
		}
		if enabled, _ := reasoning["enabled"].(bool); !enabled {
			t.Errorf("expected reasoning.enabled=true, got %v", reasoning["enabled"])
		}
		if effort, _ := reasoning["effort"].(string); effort != "high" {
			t.Errorf("expected reasoning.effort=high, got %v", reasoning["effort"])
		}
	})

	t.Run("auto mode maps to medium effort", func(t *testing.T) {
		body := []byte(`{"model":"stepfun/step-5-preview:free","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"auto"}`)
		out := ApplyNousThinking(body, "nous")

		var req map[string]any
		if err := json.Unmarshal(out, &req); err != nil {
			t.Fatalf("failed to unmarshal output: %v", err)
		}
		if _, exists := req["reasoning_effort"]; exists {
			t.Errorf("expected reasoning_effort removed, but was present in %s", out)
		}
		reasoning, ok := req["reasoning"].(map[string]any)
		if !ok {
			t.Fatalf("expected nested reasoning object in %s", out)
		}
		if enabled, _ := reasoning["enabled"].(bool); !enabled {
			t.Errorf("expected reasoning.enabled=true, got %v", reasoning["enabled"])
		}
		if effort, _ := reasoning["effort"].(string); effort != "medium" {
			t.Errorf("expected reasoning.effort=medium, got %v", reasoning["effort"])
		}
	})

	t.Run("thinking disabled omits reasoning", func(t *testing.T) {
		body := []byte(`{"model":"meituan/longcat-2.0:free","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`)
		out := ApplyNousThinking(body, "nous")

		var req map[string]any
		if err := json.Unmarshal(out, &req); err != nil {
			t.Fatalf("failed to unmarshal output: %v", err)
		}
		if _, exists := req["reasoning_effort"]; exists {
			t.Errorf("expected reasoning_effort removed, but was present in %s", out)
		}
		if _, exists := req["reasoning"]; exists {
			t.Errorf("expected reasoning omitted when disabled, but got %v", req["reasoning"])
		}
	})

	t.Run("non-nous model untouched", func(t *testing.T) {
		body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)
		out := ApplyNousThinking(body, "nous")

		var req map[string]any
		if err := json.Unmarshal(out, &req); err != nil {
			t.Fatalf("failed to unmarshal output: %v", err)
		}
		if effort, _ := req["reasoning_effort"].(string); effort != "high" {
			t.Errorf("expected reasoning_effort unchanged for non-nous model, got %v", req["reasoning_effort"])
		}
		if _, exists := req["reasoning"]; exists {
			t.Errorf("expected reasoning not added for non-nous model, but got %v", req["reasoning"])
		}
	})

	t.Run("no thinking intent leaves request untouched", func(t *testing.T) {
		body := []byte(`{"model":"meituan/longcat-2.0:free","messages":[{"role":"user","content":"hi"}]}`)
		out := ApplyNousThinking(body, "nous")
		if string(out) != string(body) {
			t.Errorf("expected body untouched when no thinking intent: got %s, want %s", out, body)
		}
	})

	t.Run("non-nous provider does not apply thinking dialect", func(t *testing.T) {
		body := []byte(`{"model":"meituan/longcat-2.0:free","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)
		out := ApplyNousThinking(body, "openai-compatible-chat-x")

		var req map[string]any
		if err := json.Unmarshal(out, &req); err != nil {
			t.Fatalf("failed to unmarshal output: %v", err)
		}
		if effort, _ := req["reasoning_effort"].(string); effort != "high" {
			t.Errorf("expected reasoning_effort unchanged for non-nous provider, got %v", req["reasoning_effort"])
		}
		if _, exists := req["reasoning"]; exists {
			t.Errorf("expected reasoning not added for non-nous provider, but got %v", req["reasoning"])
		}
	})
}

func TestNousAlias_Deduplicated(t *testing.T) {
	// providerCapabilities and ProviderModels must not have duplicate "ns" entries
	if _, exists := providerCapabilities["ns"]; exists {
		t.Error("providerCapabilities['ns'] must be dropped as ns is an alias")
	}
	if _, exists := ProviderModels["ns"]; exists {
		t.Error("ProviderModels['ns'] must be dropped as ns is an alias")
	}
	// "ns" alias must still resolve to "nous" and access its models & capabilities
	if got := ResolveAlias("ns"); got != "nous" {
		t.Errorf("ResolveAlias('ns') = %q, want 'nous'", got)
	}
	models := GetProviderModels("ns")
	if len(models) == 0 {
		t.Error("GetProviderModels('ns') returned no models")
	}
	caps := GetCapabilitiesForModel("nous", "meituan/longcat-2.0:free")
	if caps.ThinkingFormat != "nous" {
		t.Errorf("GetCapabilitiesForModel('nous') thinkingFormat = %q, want 'nous'", caps.ThinkingFormat)
	}
}
