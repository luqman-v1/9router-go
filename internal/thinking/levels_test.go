package thinking

import (
	"9router/proxy/internal/providers"
	"testing"
)

func TestLevelMaps(t *testing.T) {
	for lvl, want := range map[string]int{
		"none": 0, "minimal": 512, "low": 1024, "medium": 8192,
		"high": 24576, "xhigh": 32768, "max": 128000,
	} {
		if got, ok := EffortToBudget(lvl); !ok || got != want {
			t.Fatalf("EffortToBudget(%q) = (%d, %v), want (%d, true)", lvl, got, ok, want)
		}
		if got, ok := EffortToBudget("HIGH"); lvl == "high" && (!ok || got != want) {
			t.Fatalf("EffortToBudget case-fold = (%d, %v)", got, ok)
		}
	}
	if _, ok := EffortToBudget("bogus"); ok {
		t.Fatal("EffortToBudget(bogus) ok")
	}
	for budget, want := range map[int]string{
		0: "", -5: "", 1: "minimal", 768: "minimal", 769: "low", 4096: "low",
		4097: "medium", 16384: "medium", 16385: "high", 24576: "high",
		28672: "high", 28673: "xhigh", 80384: "xhigh", 80385: "max", 128000: "max",
	} {
		if got := BudgetToLevel(budget); got != want {
			t.Fatalf("BudgetToLevel(%d) = %q, want %q", budget, got, want)
		}
	}
	for budget, want := range map[int]string{
		0: "", 2048: "low", 2049: "medium", 16384: "medium", 16385: "high",
	} {
		if got := BudgetToEffort(budget); got != want {
			t.Fatalf("BudgetToEffort(%d) = %q, want %q", budget, got, want)
		}
	}
	for in, want := range map[string]string{
		"none": "minimal", "off": "minimal", "xhigh": "high", "max": "high", "medium": "medium",
	} {
		if got := EffortToThinkingLevel(in); got != want {
			t.Fatalf("EffortToThinkingLevel(%q) = %q, want %q", in, got, want)
		}
	}
	if got := NormalizeOpenAILevel("max", []string{"none", "minimal", "low", "medium", "high", "xhigh"}); got != "xhigh" {
		t.Fatalf("NormalizeOpenAILevel max unsupported = %q", got)
	}
	if got := NormalizeOpenAILevel("max", []string{"max"}); got != "max" {
		t.Fatalf("NormalizeOpenAILevel max supported = %q", got)
	}
	if got := NormalizeOpenAILevel("ultra", []string{"max"}); got != "max" {
		t.Fatalf("NormalizeOpenAILevel ultra→max = %q", got)
	}
	if got := NormalizeOpenAILevel("ultra", []string{"ultra"}); got != "ultra" {
		t.Fatalf("NormalizeOpenAILevel ultra kept = %q", got)
	}
	if got := ToLevel(&Intent{Mode: ModeLevel, Level: "low"}); got != "low" {
		t.Fatalf("ToLevel level = %q", got)
	}
	if got := ToLevel(&Intent{Mode: ModeBudget, Budget: 24576}); got != "high" {
		t.Fatalf("ToLevel budget = %q", got)
	}
	if got := ToLevel(&Intent{Mode: ModeBudget, Budget: -3}); got != "medium" {
		t.Fatalf("ToLevel bad budget fallback = %q", got)
	}
	if got := ToLevel(&Intent{Mode: ModeAuto}); got != "auto" {
		t.Fatalf("ToLevel auto = %q", got)
	}
	if got := ToLevel(&Intent{Mode: ModeNone}); got != "" {
		t.Fatalf("ToLevel none = %q", got)
	}
	r := &providers.ThinkingRange{Min: 1000, Max: 5000}
	if got, ok := ToBudget(&Intent{Mode: ModeBudget, Budget: 100}, r); !ok || got != 1000 {
		t.Fatalf("ToBudget clamp min = (%d, %v)", got, ok)
	}
	if got, ok := ToBudget(&Intent{Mode: ModeBudget, Budget: 99999}, r); !ok || got != 5000 {
		t.Fatalf("ToBudget clamp max = (%d, %v)", got, ok)
	}
	if got, ok := ToBudget(&Intent{Mode: ModeLevel, Level: "high"}, nil); !ok || got != 24576 {
		t.Fatalf("ToBudget level = (%d, %v)", got, ok)
	}
	if got, ok := ToBudget(&Intent{Mode: ModeAuto}, nil); !ok || got != -1 {
		t.Fatalf("ToBudget auto = (%d, %v)", got, ok)
	}
	if _, ok := ToBudget(&Intent{Mode: ModeLevel, Level: "bogus"}, nil); ok {
		t.Fatal("ToBudget bogus ok")
	}
	if _, ok := ToBudget(&Intent{Mode: ModeNone}, nil); ok {
		t.Fatal("ToBudget none ok")
	}
	if got := ToKimiReasoningEffort(&Intent{Mode: ModeAuto}); got != "high" {
		t.Fatalf("kimi auto = %q", got)
	}
	if got := ToKimiReasoningEffort(&Intent{Mode: ModeLevel, Level: "minimal"}); got != "low" {
		t.Fatalf("kimi minimal = %q", got)
	}
	if got := ToKimiReasoningEffort(&Intent{Mode: ModeLevel, Level: "xhigh"}); got != "max" {
		t.Fatalf("kimi xhigh = %q", got)
	}
}

func TestGeminiConfig(t *testing.T) {
	caps := providers.Capabilities{Reasoning: true, ThinkingFormat: "gemini-level"}
	tc, floor := GeminiConfig((&Intent{Mode: ModeLevel, Level: "high"}), "gemini-level", caps)
	if tc["thinkingLevel"] != "high" || tc["includeThoughts"] != true || floor != 65535 {
		t.Fatalf("gemini-level high = %+v floor %d", tc, floor)
	}
	tc, _ = GeminiConfig(&Intent{Mode: ModeNone}, "gemini-level", caps)
	if tc["thinkingLevel"] != "minimal" || tc["includeThoughts"] != false {
		t.Fatalf("gemini-level none = %+v", tc)
	}
	tc, floor = GeminiConfig(&Intent{Mode: ModeLevel, Level: "high"}, "gemini-budget",
		providers.Capabilities{Reasoning: true, ThinkingFormat: "gemini-budget"})
	if tc["thinkingBudget"] != 24576 || tc["includeThoughts"] != true || floor != 32768 {
		t.Fatalf("gemini-budget high = %+v floor %d", tc, floor)
	}
	tc, floor = GeminiConfig(&Intent{Mode: ModeAuto}, "gemini-budget",
		providers.Capabilities{Reasoning: true})
	if tc["thinkingBudget"] != -1 || floor != 32768 {
		t.Fatalf("gemini-budget auto = %+v floor %d", tc, floor)
	}
	tc, floor = GeminiConfig(&Intent{Mode: ModeNone}, "gemini-budget",
		providers.Capabilities{Reasoning: true})
	if tc["thinkingBudget"] != 0 || tc["includeThoughts"] != false || floor != 8192 {
		t.Fatalf("gemini-budget none = %+v floor %d", tc, floor)
	}
	if tc, floor := GeminiConfig(nil, "gemini-level", caps); tc != nil || floor != 0 {
		t.Fatalf("nil intent = %+v floor %d", tc, floor)
	}
	if tc, floor := GeminiConfig(&Intent{Mode: ModeAuto}, "openai", caps); tc != nil || floor != 0 {
		t.Fatalf("unknown format = %+v floor %d", tc, floor)
	}
}
