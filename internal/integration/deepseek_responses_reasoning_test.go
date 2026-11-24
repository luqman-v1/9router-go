//go:build integration

package integration

import (
	json "encoding/json/v2"
	"net/http"
	"testing"
)

func TestDeepSeekResponsesToolHistoryReasoning(t *testing.T) {
	env, upstream := newProviderEnv(t)
	res := env.Post(t, "/v1/responses", `{
		"model":"deepseek/deepseek-flash",
		"input":[
			{"role":"user","content":"Check the weather."},
			{"type":"function_call","call_id":"c1","name":"weather","arguments":"{}"},
			{"type":"function_call_output","call_id":"c1","output":"sunny"},
			{"type":"reasoning","content":[{"type":"reasoning_text","text":"Check the forecast too."}]},
			{"type":"function_call","call_id":"c2","name":"weather","arguments":"{}"},
			{"type":"function_call_output","call_id":"c2","output":"rain"},
			{"role":"assistant","content":"Rain tomorrow."},
			{"role":"user","content":"What about Friday?"}
		],
		"tools":[{"type":"function","name":"weather","parameters":{"type":"object","properties":{}}}]
	}`)
	if res.Status != http.StatusOK {
		t.Fatalf("Responses status = %d: %s", res.Status, truncate(res.Body))
	}
	var sent struct {
		Messages []struct {
			Role      string `json:"role"`
			Reasoning string `json:"reasoning_content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(upstream.Last(t).Body, &sent); err != nil {
		t.Fatal(err)
	}
	var reasoning []string
	for _, msg := range sent.Messages {
		if msg.Role == "assistant" {
			reasoning = append(reasoning, msg.Reasoning)
		}
	}
	if len(reasoning) != 3 || reasoning[0] != " " || reasoning[1] != "Check the forecast too." || reasoning[2] != " " {
		t.Fatalf("upstream assistant reasoning = %q; want placeholder, original text, placeholder", reasoning)
	}
}
