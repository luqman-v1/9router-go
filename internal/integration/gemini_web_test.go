//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	json "encoding/json/v2"
)

// The gemini-web executor talks to the site over its own protocol: a
// bootstrap page that carries the session tokens, a registry RPC that names
// the models the session can reach, and a StreamGenerate call that carries the
// prompt.
//
// These tests boot the real router against a fake site and assert on what the
// gateway sent and what the client received, because a unit test on the
// executor cannot see whether the connection, the model resolution and the
// answer all line up.

// geminiWebRegistryLine renders one otAQ7b line in the wire shape the site
// uses: a wrb.fr envelope whose payload is a 19-slot array with the model list
// at 15 and the tier flags at 16/17.
func geminiWebRegistryLine(id, category, displayName string) string {
	models := []any{[]any{id, category, "Fastest answers", []int{2, 140, 1}, nil, nil, nil, nil, nil, nil, nil, displayName, "Fastest answers"}}
	payload := make([]any, 19)
	payload[15] = models
	payload[16] = []int{9, 38}
	payload[17] = []int{2, 140, 115}
	encoded, _ := json.Marshal(payload)
	envelope, _ := json.Marshal([]any{[]any{"wrb.fr", "otAQ7b", string(encoded)}})
	return string(envelope)
}

// geminiWebStreamBody is a whole answer as the site streams it: the answer
// grows across frames, the identity rides the first one, and the terminal
// marker closes it.
func geminiWebStreamBody(answer, modelID string) string {
	var sb strings.Builder
	sb.WriteString(")]}'\n\n")
	for i := 1; i <= len(answer); i++ {
		candidate := make([]any, 9)
		candidate[0] = "rc_test"
		candidate[1] = []any{answer[:i]}
		payload := make([]any, 5)
		payload[1] = []any{"c_test", "r_test"}
		payload[4] = []any{candidate}
		payload = append(payload, map[string]any{"40": modelID, "26": "ctx-token"})
		encoded, _ := json.Marshal(payload)
		envelope, _ := json.Marshal([]any{[]any{"wrb.fr", nil, string(encoded)}})
		sb.WriteString("177\n" + string(envelope) + "\n")
	}
	sb.WriteString(`[["e",11,null,null,22118]]` + "\n")
	return sb.String()
}

// geminiWebFakeSite serves the three endpoints the executor calls.
func geminiWebFakeSite(t *testing.T, modelID, answer string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/batchexecute"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(")]}'\n\n" + geminiWebRegistryLine(modelID, "Flash-Lite", "3.5 Flash-Lite") + "\n"))
		case strings.Contains(r.URL.Path, "StreamGenerate"):
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(geminiWebStreamBody(answer, modelID)))
		case strings.HasSuffix(r.URL.Path, "/app"):
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><script>window.WIZ_global_data = {"cfb2h":"boq_test-bl","FdrFJe":"1234567890","Ylro7b":"pctx"}}</script></html>`))
		default:
			http.NotFound(w, r)
		}
	}
}

// TestGeminiWebServesAChatClient is the contract a client depends on: a
// request for a gemini-web model reaches the site, and the answer comes back
// as a chat completion the client can read.
func TestGeminiWebServesAChatClient(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, geminiWebFakeSite(t, "cf41b0e0dd7d53e5", "the site answered"))
	// The executor derives every endpoint from the base URL, so the fake site
	// has to be the origin of the configured base URL.
	env.AddConnection(t, "conn-gemini-web", "gemini-web", "Gemini Web", upstream, "")

	res := env.Post(t, "/v1/chat/completions", map[string]any{
		"model":  "gemini-web/gemini-3.1-flash-lite",
		"stream": false,
		"messages": []map[string]any{
			{"role": "user", "content": "hello"},
		},
	})
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	var completion struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	res.Decode(t, &completion)
	if completion.Object != "chat.completion" {
		t.Errorf("object = %q, want \"chat.completion\"", completion.Object)
	}
	if len(completion.Choices) != 1 {
		t.Fatalf("choices = %d, want 1 (body: %s)", len(completion.Choices), truncate(res.Body))
	}
	if got := completion.Choices[0].Message.Content; !strings.Contains(got, "the site answered") {
		t.Errorf("content = %q, want the site's answer", got)
	}
	if completion.Usage.PromptTokens == 0 {
		t.Error("prompt_tokens = 0, want an estimate of the prompt that was sent")
	}
	if completion.Usage.CompletionTokens == 0 {
		t.Error("completion_tokens = 0, want an estimate of the answer that came back")
	}
}

// TestGeminiWebServesAStreamingChatClient pins the streamed shape.
//
// A streaming client reads frames, and a provider that answered with one
// document would leave it waiting for a terminator that never comes.
func TestGeminiWebServesAStreamingChatClient(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, geminiWebFakeSite(t, "cf41b0e0dd7d53e5", "the site answered"))
	env.AddConnection(t, "conn-gemini-web", "gemini-web", "Gemini Web", upstream, "")

	res := env.Post(t, "/v1/chat/completions", map[string]any{
		"model":  "gemini-web/gemini-3.1-flash-lite",
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": "hello"},
		},
	})
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	body := string(res.Body)
	if !strings.Contains(body, "chat.completion.chunk") {
		t.Errorf("the stream carries no chat completion chunk:\n%s", truncate(res.Body))
	}
	if got := geminiWebStreamedContent(t, body); got != "the site answered" {
		t.Errorf("the streamed answer = %q, want %q", got, "the site answered")
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("the stream carries no terminator:\n%s", truncate(res.Body))
	}
}

// geminiWebStreamedContent reassembles the answer a client reads out of a
// chat completion stream.
//
// The answer arrives as one delta per character, so the content is only
// readable once the frames are put back together — which is exactly what a
// client does.
func geminiWebStreamedContent(t *testing.T, body string) string {
	t.Helper()
	var answer strings.Builder
	for _, line := range strings.Split(body, "\n") {
		payload := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		for _, choice := range chunk.Choices {
			answer.WriteString(choice.Delta.Content)
		}
	}
	return answer.String()
}
