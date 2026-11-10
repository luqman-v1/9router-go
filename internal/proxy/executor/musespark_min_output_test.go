package executor

import (
	json "encoding/json/v2"
	"testing"
)

// #349: Muse Spark consumes 570-1320 hidden reasoning tokens before it emits a
// single completion token, so a caller-set output budget below the 512 floor is
// spent entirely on reasoning. The upstream then answers 200 with an empty
// message — which the gateway reports as "Provider returned empty content" —
// or stalls the stream until the watchdog fires a 502. Neither failure names the
// budget, so from the caller's side a working model looks broken.

func TestClampMuseSparkOutputBudgetRaisesOnlyTinyCallerBudgets(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		body       map[string]any
		wantRaised []string
		wantBody   map[string]any
	}{
		{
			name:       "budget below the floor is raised",
			model:      "muse-spark-1.3-contributor-free",
			body:       map[string]any{"max_tokens": int64(64)},
			wantRaised: []string{"max_tokens"},
			wantBody:   map[string]any{"max_tokens": museSparkMinOutputTokens},
		},
		{
			name:       "one token below the floor is raised",
			model:      "muse-spark-1.3",
			body:       map[string]any{"max_output_tokens": int64(511)},
			wantRaised: []string{"max_output_tokens"},
			wantBody:   map[string]any{"max_output_tokens": museSparkMinOutputTokens},
		},
		{
			name:     "a budget exactly at the floor is untouched",
			model:    "muse-spark-1.3",
			body:     map[string]any{"max_tokens": int64(512)},
			wantBody: map[string]any{"max_tokens": int64(512)},
		},
		{
			name:     "an explicit larger budget is untouched",
			model:    "muse-spark-1.3",
			body:     map[string]any{"max_tokens": int64(128000)},
			wantBody: map[string]any{"max_tokens": int64(128000)},
		},
		{
			name:     "no budget is not synthesized",
			model:    "muse-spark-1.3",
			body:     map[string]any{"input": "hi"},
			wantBody: map[string]any{"input": "hi"},
		},
		{
			name:     "a string budget is not a budget",
			model:    "muse-spark-1.3",
			body:     map[string]any{"max_tokens": "64"},
			wantBody: map[string]any{"max_tokens": "64"},
		},
		{
			name:       "all three spellings are clamped in one body",
			model:      "muse-spark-1.3",
			body:       map[string]any{"max_tokens": int64(10), "max_output_tokens": float64(20), "max_completion_tokens": int(30)},
			wantRaised: []string{"max_tokens", "max_output_tokens", "max_completion_tokens"},
			wantBody: map[string]any{
				"max_tokens":            museSparkMinOutputTokens,
				"max_output_tokens":     museSparkMinOutputTokens,
				"max_completion_tokens": museSparkMinOutputTokens,
			},
		},
		{
			name:     "a sibling model on the same lane keeps its budget",
			model:    "grok-4.6",
			body:     map[string]any{"max_tokens": int64(64)},
			wantBody: map[string]any{"max_tokens": int64(64)},
		},
		{
			name:     "a non-spark model keeps its budget",
			model:    "big-pickle",
			body:     map[string]any{"max_tokens": int64(64)},
			wantBody: map[string]any{"max_tokens": int64(64)},
		},
		{
			name:     "no budget map is a no-op",
			model:    "muse-spark-1.3",
			body:     nil,
			wantBody: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raised := clampMuseSparkOutputBudget(tt.body, tt.model)
			if !sameStrings(raised, tt.wantRaised) {
				t.Errorf("raised = %v, want %v", raised, tt.wantRaised)
			}
			if len(tt.body) != len(tt.wantBody) {
				t.Fatalf("body = %v, want %v", tt.body, tt.wantBody)
			}
			for key, want := range tt.wantBody {
				got, ok := tt.body[key]
				if !ok {
					t.Fatalf("body lost key %q (body: %v)", key, tt.body)
				}
				if !sameValue(got, want) {
					t.Errorf("body[%q] = %#v, want %#v", key, got, want)
				}
			}
		})
	}
}

// TestNormalizeMuseSparkResponsesBodyAppliesTheFloor drives the real entry point
// with the JSON a client sends, so the clamp is proven to run on the path the
// executor takes rather than only as an isolated helper.
func TestNormalizeMuseSparkResponsesBodyAppliesTheFloor(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		body     string
		wantKey  string
		wantLess bool
	}{
		{
			name:     "tiny max_output_tokens is raised",
			model:    "muse-spark-1.3-contributor-free",
			body:     `{"model":"muse-spark-1.3-contributor-free","max_output_tokens":100,"input":[]}`,
			wantKey:  "max_output_tokens",
			wantLess: false,
		},
		{
			name:     "tiny max_tokens is raised on the prefixed id",
			model:    "oc/muse-spark-1.2-contributor-free",
			body:     `{"model":"oc/muse-spark-1.2-contributor-free","max_tokens":64,"input":[]}`,
			wantKey:  "max_tokens",
			wantLess: false,
		},
		{
			name:     "a large budget survives",
			model:    "muse-spark-1.3",
			body:     `{"model":"muse-spark-1.3","max_output_tokens":128000,"input":[]}`,
			wantKey:  "max_output_tokens",
			wantLess: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := normalizeMuseSparkResponsesBody([]byte(tt.body), tt.model)
			if err != nil {
				t.Fatalf("normalizeMuseSparkResponsesBody: %v", err)
			}

			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal normalized body: %v", err)
			}
			got, ok := outputBudgetOf(m[tt.wantKey])
			if !ok {
				t.Fatalf("normalized body has no numeric %s: %s", tt.wantKey, out)
			}
			if tt.wantLess {
				if got != 128000 {
					t.Errorf("%s = %d, want the caller's 128000 untouched", tt.wantKey, got)
				}
				return
			}
			if got != museSparkMinOutputTokens {
				t.Errorf("%s = %d, want the %d floor", tt.wantKey, got, museSparkMinOutputTokens)
			}
		})
	}
}

// TestNormalizeMuseSparkResponsesBodyHonoursTheEffortSuffix covers the second
// half of the port: the "(level)" the client appends to the model id is 9router
// request notation and never reaches the wire, but the intent must not be lost
// with it. Fill-only precedence — an effort the body states explicitly is left
// exactly as the caller wrote it.
func TestNormalizeMuseSparkResponsesBodyHonoursTheEffortSuffix(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		body       string
		wantEffort string
	}{
		{
			name:       "suffix fills an absent effort",
			model:      "muse-spark-1.3(high)",
			body:       `{"max_tokens":512,"input":[]}`,
			wantEffort: "high",
		},
		{
			name:       "suffix survives the client prefix",
			model:      "oc/muse-spark-1.3-contributor-free(medium)",
			body:       `{"max_tokens":512,"input":[]}`,
			wantEffort: "medium",
		},
		{
			name:       "explicit body effort wins over the suffix",
			model:      "muse-spark-1.3(high)",
			body:       `{"max_tokens":512,"reasoning_effort":"low","input":[]}`,
			wantEffort: "low",
		},
		{
			name:       "explicit reasoning object wins over the suffix",
			model:      "muse-spark-1.3(low)",
			body:       `{"max_tokens":512,"reasoning":{"effort":"high"},"input":[]}`,
			wantEffort: "high",
		},
		{
			name:       "no suffix and no body effort leaves reasoning unset",
			model:      "muse-spark-1.3",
			body:       `{"max_tokens":512,"input":[]}`,
			wantEffort: "",
		},
		{
			name:       "an unknown suffix is not an effort",
			model:      "muse-spark-1.3(turbo)",
			body:       `{"max_tokens":512,"input":[]}`,
			wantEffort: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := normalizeMuseSparkResponsesBody([]byte(tt.body), tt.model)
			if err != nil {
				t.Fatalf("normalizeMuseSparkResponsesBody: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal normalized body: %v", err)
			}

			reasoning, _ := m["reasoning"].(map[string]any)
			got, _ := reasoning["effort"].(string)
			if got != tt.wantEffort {
				t.Fatalf("reasoning.effort = %q, want %q (body: %s)", got, tt.wantEffort, out)
			}
			if tt.wantEffort != "" && reasoning["summary"] != "auto" {
				t.Errorf("reasoning.summary = %v, want \"auto\"", reasoning["summary"])
			}
		})
	}
}

func TestMuseSparkEffortSuffix(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "muse-spark-1.3(high)", want: "high"},
		{in: "muse-spark-1.3(HIGH)", want: "high"},
		{in: "muse-spark-1.3 ( low )", want: "low"},
		{in: "muse-spark-1.3(medium)", want: "medium"},
		{in: "muse-spark-1.3(xhigh)", want: ""},
		{in: "muse-spark-1.3(max)", want: ""},
		{in: "muse-spark-1.3", want: ""},
		{in: "muse-spark-1.3(high", want: ""},
		{in: "grok-4.6(high)", want: "high"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := museSparkEffortSuffix(tt.in); got != tt.want {
				t.Errorf("museSparkEffortSuffix(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMuseSparkCleanModel(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "muse-spark-1.3", want: "muse-spark-1.3"},
		{in: "oc/muse-spark-1.3", want: "muse-spark-1.3"},
		{in: "opencode/muse-spark-1.2-contributor-free", want: "muse-spark-1.2-contributor-free"},
		{in: "opencode-go/muse-spark-1.3", want: "muse-spark-1.3"},
		{in: "oc/muse-spark-1.3(high)", want: "muse-spark-1.3"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := museSparkCleanModel(tt.in); got != tt.want {
				t.Errorf("museSparkCleanModel(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func sameValue(got, want any) bool {
	gotNum, gotOK := outputBudgetOf(got)
	wantNum, wantOK := outputBudgetOf(want)
	if gotOK && wantOK {
		return gotNum == wantNum
	}
	return got == want
}
