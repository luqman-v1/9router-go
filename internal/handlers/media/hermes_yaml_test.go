package media

import (
	"strings"
	"testing"
)

func TestUpsertModelBlock_ReplacesScalarSentinel(t *testing.T) {
	// A fresh Hermes install ships `model: ""`. Leaving it behind would give
	// the file two `model:` keys and the last-wins loader would silently drop
	// the block we just wrote.
	in := "model: \"\"\ntelemetry: false\n"
	got := upsertModelBlock(in, buildModelBlock("p/m", "http://127.0.0.1:20130/v1"))

	if strings.Contains(got, `model: ""`) {
		t.Fatalf("scalar sentinel survived the upsert:\n%s", got)
	}
	topLevel := 0
	for i, line := range strings.Split(got, "\n") {
		if i == 0 || strings.HasPrefix(line, "model:") {
			topLevel++
		}
	}
	if topLevel != 1 {
		t.Fatalf("want exactly one top-level model key, got %d:\n%s", topLevel, got)
	}
	if !strings.Contains(got, "telemetry: false") {
		t.Errorf("unrelated key was dropped:\n%s", got)
	}
}

func TestUpsertModelBlock_PreservesForeignKeysAndOrder(t *testing.T) {
	in := "# my notes\nprovider_cache: 5\nmodel:\n  default: \"old/m\"\n  provider: \"custom\"\nother: keep\n"
	got := upsertModelBlock(in, buildModelBlock("new/m", "http://127.0.0.1:20130/v1"))

	for _, want := range []string{"# my notes", "provider_cache: 5", "other: keep", `default: "new/m"`} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "old/m") {
		t.Errorf("old model survived:\n%s", got)
	}
}

func TestUpsertModelBlock_AppendsWhenAbsent(t *testing.T) {
	got := upsertModelBlock("telemetry: false\n", buildModelBlock("p/m", "http://x/v1"))
	if !strings.HasPrefix(got, "model:") {
		t.Fatalf("block should be prepended:\n%s", got)
	}
	if !strings.Contains(got, "telemetry: false") {
		t.Errorf("existing content lost:\n%s", got)
	}
}

func TestParseHermesModelBlock_DoesNotMatchModelAliases(t *testing.T) {
	// Anchored without leading indent and requiring ':' right after "model",
	// so a `model_aliases:` key can never be read as the model block.
	got := parseHermesModelBlock("model_aliases:\n  m: \"x\"\n")
	if got != nil {
		t.Fatalf("model_aliases parsed as a model block: %+v", got)
	}
}

func TestParseHermesModelBlock_ScalarSentinelIsUnset(t *testing.T) {
	if got := parseHermesModelBlock("model: \"\"\n"); got != nil {
		t.Fatalf("sentinel should parse as unset, got %+v", got)
	}
}

func TestRemoveModelBlock_ClearsSentinel(t *testing.T) {
	got := removeModelBlock("model: \"\"\nother: keep\n")
	if strings.Contains(got, "model") {
		t.Fatalf("sentinel survived removal: %q", got)
	}
	if !strings.Contains(got, "other: keep") {
		t.Fatalf("unrelated key removed: %q", got)
	}
}

func TestAuxRoles_UpsertCreatesSectionAndKeepsSiblings(t *testing.T) {
	yaml := upsertHermesAuxRole("", "compression", buildAuxRoleBlock("compression", "p/m", "http://x/v1"))
	if !strings.HasPrefix(yaml, "auxiliary:") {
		t.Fatalf("section not created: %q", yaml)
	}

	yaml = upsertHermesAuxRole(yaml, "review", buildAuxRoleBlock("review", "p/r", "http://y/v1"))
	roles := parseHermesAuxRoles(yaml)
	if len(roles) != 2 {
		t.Fatalf("want 2 roles, got %d: %+v", len(roles), roles)
	}
	if roles["compression"].Model == nil || *roles["compression"].Model != "p/m" {
		t.Errorf("compression model = %v, want p/m", roles["compression"].Model)
	}
	if roles["review"].Model == nil || *roles["review"].Model != "p/r" {
		t.Errorf("review model = %v, want p/r", roles["review"].Model)
	}
}

func TestRemoveHermesAuxRole_DropsSectionWhenEmpty(t *testing.T) {
	yaml := upsertHermesAuxRole("telemetry: false\n", "compression", buildAuxRoleBlock("compression", "p/m", "http://x/v1"))
	got := removeHermesAuxRole(yaml, "compression")

	if strings.Contains(got, "auxiliary:") {
		t.Fatalf("empty section survived: %q", got)
	}
	if !strings.Contains(got, "telemetry: false") {
		t.Fatalf("unrelated key removed: %q", got)
	}
}

func TestRemoveHermesAuxRole_KeepsSiblings(t *testing.T) {
	yaml := upsertHermesAuxRole("", "compression", buildAuxRoleBlock("compression", "p/m", "http://x/v1"))
	yaml = upsertHermesAuxRole(yaml, "review", buildAuxRoleBlock("review", "p/r", "http://y/v1"))

	got := removeHermesAuxRole(yaml, "compression")
	roles := parseHermesAuxRoles(got)
	if _, ok := roles["compression"]; ok {
		t.Errorf("compression survived: %+v", roles)
	}
	if _, ok := roles["review"]; !ok {
		t.Errorf("review was removed too: %+v", roles)
	}
}

func TestUpsertEnvVar_ReplacesOnlyThatLine(t *testing.T) {
	in := "TELEGRAM_BOT_TOKEN=abc\nOPENAI_API_KEY=old\nOTHER=1\n"
	got := upsertEnvVar(in, "OPENAI_API_KEY", "new")

	if !strings.Contains(got, "OPENAI_API_KEY=new\n") {
		t.Errorf("key not updated: %q", got)
	}
	if strings.Contains(got, "old") {
		t.Errorf("old value survived: %q", got)
	}
	for _, want := range []string{"TELEGRAM_BOT_TOKEN=abc", "OTHER=1"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q: %q", want, got)
		}
	}
}

func TestUpsertEnvVar_AppendsWithoutNewline(t *testing.T) {
	got := upsertEnvVar("A=1", "OPENAI_API_KEY", "k")
	if got != "A=1\nOPENAI_API_KEY=k\n" {
		t.Fatalf("got %q", got)
	}
}

func TestHas9RouterConfig_OnlyLocalCustomEndpoints(t *testing.T) {
	custom, other := "custom", "openrouter"

	tests := []struct {
		name     string
		provider *string
		baseURL  *string
		want     bool
	}{
		{"local custom", &custom, new("http://127.0.0.1:20130/v1"), true},
		{"localhost custom", &custom, new("http://localhost:20130/v1"), true},
		{"tunnel custom reads false", &custom, new("https://abc.trycloudflare.com/v1"), false},
		{"foreign provider", &other, new("http://127.0.0.1:20130/v1"), false},
		{"no endpoint", &custom, nil, false},
		{"nil block fields", nil, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := has9RouterConfig(tt.provider, tt.baseURL); got != tt.want {
				t.Fatalf("has9RouterConfig = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsCustomBlock(t *testing.T) {
	if isCustomBlock(nil) {
		t.Error("nil provider is not a custom block")
	}
	if isCustomBlock(new("openai")) {
		t.Error("openai is not a custom block")
	}
	if !isCustomBlock(new("custom")) {
		t.Error("custom must be recognised")
	}
}
