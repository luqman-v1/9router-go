package executor

import (
	"testing"

	json "encoding/json/v2"
)

// The Codex backend accepts one service_tier. Upstream's rule, ported from
// open-sse/executors/codex.js:
//
//	if (body.service_tier && body.service_tier !== "priority") delete body.service_tier;
//	if (body.service_tier === "fast") body.service_tier = "priority";
//	delete body.speed;
//
// The guard is truthiness, not "not priority": null, false, 0 and "" survive.
// A client that sends an explicit null keeps the field, and reading the rule
// as "delete everything that is not priority" would be stricter than the
// reference.
func TestNormalizeCodexServiceTier(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string // the expected body, re-serialised from the map
		absent  bool
		present bool
	}{
		{name: "fast is renamed to priority", in: `{"service_tier":"fast"}`, want: `"service_tier":"priority"`, present: true},
		{name: "priority is kept", in: `{"service_tier":"priority"}`, want: `"service_tier":"priority"`, present: true},
		{name: "flex is deleted", in: `{"service_tier":"flex"}`, absent: true},
		{name: "standard is deleted", in: `{"service_tier":"standard"}`, absent: true},
		{name: "an explicit null survives", in: `{"service_tier":null}`, want: `"service_tier":null`, present: true},
		{name: "false survives", in: `{"service_tier":false}`, want: `"service_tier":false`, present: true},
		{name: "zero survives", in: `{"service_tier":0}`, want: `"service_tier":0`, present: true},
		{name: "an empty string survives", in: `{"service_tier":""}`, want: `"service_tier":""`, present: true},
		{name: "an absent tier stays absent", in: `{}`, absent: true},
		{name: "a number is truthy and deleted", in: `{"service_tier":1}`, absent: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var envelope map[string]any
			if err := json.Unmarshal([]byte(tc.in), &envelope); err != nil {
				t.Fatalf("decode fixture: %v", err)
			}
			normalizeCodexServiceTier(envelope)

			got, has := envelope["service_tier"]
			switch {
			case tc.absent && has:
				t.Fatalf("service_tier = %v, want the field deleted", got)
			case tc.absent:
				// correct
			case !has:
				t.Fatalf("service_tier was deleted, want it kept")
			}

			if tc.present {
				encoded, err := json.Marshal(got)
				if err != nil {
					t.Fatalf("encode value: %v", err)
				}
				if want := tc.want[len(`"service_tier":`):]; string(encoded) != want {
					t.Fatalf("service_tier = %s, want %s", encoded, want)
				}
			}
		})
	}
}

// The client-only speed hint never reaches the Codex backend, whatever else the
// body carries.
func TestNormalizeCodexServiceTier_DropsSpeed(t *testing.T) {
	var envelope map[string]any
	if err := json.Unmarshal([]byte(`{"speed":"fast","model":"gpt-6-sol"}`), &envelope); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	normalizeCodexServiceTier(envelope)

	if _, ok := envelope["speed"]; ok {
		t.Fatal("speed must not reach the Codex backend")
	}
	if envelope["model"] != "gpt-6-sol" {
		t.Fatalf("model must survive, got %v", envelope["model"])
	}
}

// The rule belongs to the Codex path only. buildResponsesBody is shared with
// the other Responses providers, and grok-cli's backend accepts the field as
// sent — so the shared builder must not apply the Codex allowlist.
func TestNormalizeCodexServiceTier_IsNotInBuildResponsesBody(t *testing.T) {
	raw := []byte(`{"model":"grok-4.5","service_tier":"flex","messages":[{"role":"user","content":"hi"}]}`)

	transformed, _, err := buildResponsesBody(raw)
	if err != nil {
		t.Fatalf("buildResponsesBody failed: %v", err)
	}
	parsed := decodeBody(t, transformed)
	if got := parsed["service_tier"]; got != "flex" {
		t.Fatalf("service_tier = %v, want flex kept by the shared builder", got)
	}
}
