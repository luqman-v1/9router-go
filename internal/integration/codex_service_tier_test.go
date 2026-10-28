//go:build integration

package integration

import (
	"net/http"
	"testing"

	json "encoding/json/v2"
)

// codexTierUpstream answers a Codex turn with the smallest stream a client
// accepts: one content delta and the terminal. It exists so a test can read the
// request the gateway sent without the response path mattering.
func codexTierUpstream(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[]}}\n\n"))
}

// codexTierEnv is codexEnv plus the fake upstream, because these tests assert
// on the outbound body rather than only on what the client received.
func codexTierEnv(t *testing.T) (*Env, *Upstream) {
	t.Helper()
	env := newEnv(t)
	up := env.NewUpstream(t, codexTierUpstream)

	priority := 1
	data := `{"apiKey":"sk-codex","baseUrl":"` + up.URL + `/responses"}`
	if err := env.Repo.CreateProviderConnectionFull("conn-codex", "codex", "apikey", "Codex Integration", &priority, data); err != nil {
		t.Fatalf("create codex connection: %v", err)
	}
	return env, up
}

// sentBody decodes what the gateway actually sent upstream, asserting there was
// exactly one request: two would mean a retry or a fallback, and the body
// assertions would be reading the wrong turn.
func sentBody(t *testing.T, up *Upstream) map[string]any {
	t.Helper()
	if n := up.Count(); n != 1 {
		t.Fatalf("upstream received %d requests, want exactly 1", n)
	}
	req := up.Last(t)
	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("decode upstream body %q: %v", truncate(req.Body), err)
	}
	return body
}

// assertResponsesShape checks the Chat→Responses conversion actually ran, so a
// service_tier assertion below cannot be satisfied by a body that was relayed
// as Chat Completions instead.
func assertResponsesShape(t *testing.T, sent map[string]any) {
	t.Helper()
	if _, has := sent["input"]; !has {
		t.Fatalf("upstream body has no input[]; the conversion did not run: %v", sent)
	}
	if _, has := sent["messages"]; has {
		t.Fatalf("upstream body still carries messages[]: %v", sent)
	}
	if _, has := sent["speed"]; has {
		t.Fatalf("speed is a client hint and must not reach Codex: %v", sent)
	}
}

// TestCodexServiceTierSurvivesChatToResponses is the fix test for the drop that
// #450 and #456 both hit: ApplyCodexFastMode set service_tier on the
// client-facing Chat Completions body, and buildResponsesBody rebuilt the
// outgoing request field by field without it, so the flag never reached the
// upstream on the path most clients take. Every case sends `messages`, not
// `input`, because the conversion is the thing under test.
func TestCodexServiceTierSurvivesChatToResponses(t *testing.T) {
	cases := []struct {
		name    string
		present bool   // true when the client sends a service_tier at all
		tier    any    // the value sent when present
		want    any    // meaningful only when absent is false
		absent  bool   // true when the upstream body must not carry the field
		speed   string // non-empty adds the client-only speed hint
		comment string
	}{
		{
			name:    "fast becomes priority",
			present: true,
			tier:    "fast",
			want:    "priority",
			comment: "open-sse/executors/codex.js renames it before the allowlist",
		},
		{
			name:    "priority is kept",
			present: true,
			tier:    "priority",
			want:    "priority",
			comment: "the one value the backend accepts passes through untouched",
		},
		{
			name:    "flex is dropped",
			present: true,
			tier:    "flex",
			absent:  true,
			comment: "upstream deletes a truthy value that is not priority",
		},
		{
			name:    "standard is dropped",
			present: true,
			tier:    "standard",
			absent:  true,
			comment: "same rule, different spelling",
		},
		{
			name:    "no tier stays absent",
			absent:  true,
			comment: "nothing invents a tier the caller did not ask for",
		},
		{
			name:    "speed alone does not promote",
			speed:   "fast",
			absent:  true,
			comment: "speed is a client hint, not a Codex service_tier",
		},
		{
			name:    "empty string is falsy and survives",
			present: true,
			tier:    "",
			want:    "",
			comment: "upstream's guard is truthiness, so \"\" is not deleted",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, up := codexTierEnv(t)
			payload := map[string]any{
				"model":  "cx/gpt-6-sol",
				"stream": true,
				"messages": []map[string]any{
					{"role": "user", "content": "ping"},
				},
			}
			if tc.present {
				payload["service_tier"] = tc.tier
			}
			if tc.speed != "" {
				payload["speed"] = tc.speed
			}

			res := env.Post(t, "/v1/chat/completions", payload)
			if res.Status != http.StatusOK {
				t.Fatalf("status = %d, body = %s", res.Status, truncate(res.Body))
			}

			sent := sentBody(t, up)
			assertResponsesShape(t, sent)

			got, has := sent["service_tier"]
			switch {
			case tc.absent && has:
				t.Fatalf("service_tier = %v, want the field absent (%s)", got, tc.comment)
			case tc.absent:
				// correct
			case !has:
				t.Fatalf("service_tier missing from the upstream body; want %v (%s)", tc.want, tc.comment)
			case got != tc.want:
				t.Fatalf("service_tier = %v, want %v (%s)", got, tc.want, tc.comment)
			}
		})
	}
}

// TestCodexServiceTierNullIsPreserved pins the part of upstream's rule the
// review read too broadly: codex.js guards on truthiness, so a client that
// sends an explicit null keeps the field. Delete-everything-not-priority would
// be stricter than the reference and would change what that client gets.
func TestCodexServiceTierNullIsPreserved(t *testing.T) {
	env, up := codexTierEnv(t)
	res := env.Post(t, "/v1/chat/completions", map[string]any{
		"model":        "cx/gpt-6-sol",
		"stream":       true,
		"service_tier": nil,
		"messages": []map[string]any{
			{"role": "user", "content": "ping"},
		},
	})
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Status, truncate(res.Body))
	}
	sent := sentBody(t, up)
	assertResponsesShape(t, sent)
	got, has := sent["service_tier"]
	if !has {
		t.Fatalf("an explicit null must survive the conversion; the field was dropped: %v", sent)
	}
	if got != nil {
		t.Fatalf("service_tier = %v, want null", got)
	}
}

// TestCodexServiceTierNativeResponses covers the client that already speaks the
// Responses API. That body takes the other branch of buildResponsesBody — the
// map is carried through, not rebuilt — so the tier arrives on its own and the
// normalize has to reach it there too.
func TestCodexServiceTierNativeResponses(t *testing.T) {
	cases := []struct {
		name   string
		tier   any
		want   any
		absent bool
	}{
		{name: "fast becomes priority", tier: "fast", want: "priority"},
		{name: "flex is dropped", tier: "flex", absent: true},
		{name: "priority is kept", tier: "priority", want: "priority"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, up := codexTierEnv(t)
			res := env.Post(t, "/v1/responses", map[string]any{
				"model":        "cx/gpt-6-sol",
				"stream":       true,
				"service_tier": tc.tier,
				"input": []map[string]any{
					{"role": "user", "content": []map[string]any{{"type": "input_text", "text": "ping"}}},
				},
			})
			if res.Status != http.StatusOK {
				t.Fatalf("status = %d, body = %s", res.Status, truncate(res.Body))
			}

			sent := sentBody(t, up)
			got, has := sent["service_tier"]
			switch {
			case tc.absent && has:
				t.Fatalf("service_tier = %v, want the field absent", got)
			case tc.absent:
				// correct
			case !has:
				t.Fatalf("service_tier missing from the upstream body; want %v", tc.want)
			case got != tc.want:
				t.Fatalf("service_tier = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSiblingResponsesProviderKeepsFlex proves the normalize lives on the Codex
// path and not in the shared builder. grok-cli goes through the same
// buildResponsesBody; if the rule were applied there, a flex sent to it would
// disappear too.
func TestSiblingResponsesProviderKeepsFlex(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, codexTierUpstream)

	priority := 1
	data := `{"apiKey":"sk-grok","baseUrl":"` + up.URL + `/responses"}`
	if err := env.Repo.CreateProviderConnectionFull("conn-grok", "grok-cli", "apikey", "Grok CLI", &priority, data); err != nil {
		t.Fatalf("create grok-cli connection: %v", err)
	}

	res := env.Post(t, "/v1/chat/completions", map[string]any{
		"model":        "gcli/grok-4.5",
		"stream":       true,
		"service_tier": "flex",
		"messages": []map[string]any{
			{"role": "user", "content": "ping"},
		},
	})
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Status, truncate(res.Body))
	}
	sent := sentBody(t, up)
	if got := sent["service_tier"]; got != "flex" {
		t.Fatalf("service_tier = %v, want flex kept — the Codex rule must not leak into the shared builder", got)
	}
}
