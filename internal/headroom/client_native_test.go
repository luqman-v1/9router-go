package headroom

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Native Responses bodies keep tool results in input[] rather than messages;
// upstream a440ef43 projects their plain-text leaves into temporary OpenAI
// tool messages, compresses those, and writes shorter text back.

func TestHeadroomClient_NativeToolOutputCompression(t *testing.T) {
	longText := "a very long plain tool output that the compressor should shorten considerably"

	t.Run("plainTextFunctionCallOutputIsCompressedAndWrittenBack", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Messages []map[string]any `json:"messages"`
			}
			_ = json.Unmarshal(readAll(r), &req)
			if len(req.Messages) != 1 {
				t.Errorf("expected 1 projected message, got %d", len(req.Messages))
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"messages": [{"role": "tool", "tool_call_id": %q, "content": "short"}], "tokens_saved": 42}`,
				req.Messages[0]["tool_call_id"])
		}))
		defer ts.Close()

		body := fmt.Sprintf(`{"model": "gpt-5.6", "input": [
			{"type": "function_call", "call_id": "c1", "name": "search", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "c1", "output": %q}
		]}`, longText)

		client := NewClient(ts.URL, "key", nil)
		resBody, compressed, err := client.Compress(context.Background(), []byte(body), "gpt-5.6", 1000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !compressed {
			t.Fatalf("expected compressed=true")
		}
		var parsed map[string]any
		if err := json.Unmarshal(resBody, &parsed); err != nil {
			t.Fatalf("returned body does not parse: %v", err)
		}
		input := parsed["input"].([]any)
		output := input[1].(map[string]any)["output"].(string)
		if output != "short" {
			t.Errorf("output = %q, want the compressed text", output)
		}
		// The protocol item next to the leaf keeps its native shape.
		if got := input[0].(map[string]any)["type"]; got != "function_call" {
			t.Errorf("function_call item type = %v, want function_call", got)
		}
	})

	t.Run("jsonPayloadAndMessageOnlyInputStayNative", func(t *testing.T) {
		calls := 0
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Write([]byte(`{"messages": []}`))
		}))
		defer ts.Close()

		client := NewClient(ts.URL, "key", nil)

		jsonBody := []byte(`{"model": "gpt-5.6", "input": [
			{"type": "function_call_output", "call_id": "c1", "output": "{\"result\": 1}"}
		]}`)
		if _, compressed, _ := client.Compress(context.Background(), jsonBody, "gpt-5.6", 1000); compressed {
			t.Error("a JSON payload must not be projected")
		}

		messageBody := []byte(`{"model": "gpt-5.6", "input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "long text here"}]}
		]}`)
		if _, compressed, _ := client.Compress(context.Background(), messageBody, "gpt-5.6", 1000); compressed {
			t.Error("an all-message input must not be projected")
		}
		if calls != 0 {
			t.Errorf("compressor called %d times, want 0", calls)
		}
	})

	t.Run("mismatchedResponseIsRejected", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"messages": [{"role": "tool", "tool_call_id": "headroom-tool-999", "content": "short"}], "tokens_saved": 42}`))
		}))
		defer ts.Close()

		body := fmt.Sprintf(`{"model": "gpt-5.6", "input": [
			{"type": "function_call_output", "call_id": "c1", "output": %q}
		]}`, longText)

		client := NewClient(ts.URL, "key", nil)
		resBody, compressed, err := client.Compress(context.Background(), []byte(body), "gpt-5.6", 1000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if compressed {
			t.Fatal("a mismatched projection response must not be applied")
		}
		if !strings.Contains(string(resBody), longText) {
			t.Error("original output text must survive a rejected compression")
		}
	})

	t.Run("longerOrEmptyResponseDoesNotOverwrite", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Messages []map[string]any `json:"messages"`
			}
			_ = json.Unmarshal(readAll(r), &req)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"messages": [{"role": "tool", "tool_call_id": %q, "content": %q}], "tokens_saved": 3}`,
				req.Messages[0]["tool_call_id"], longText+" and now even longer")
		}))
		defer ts.Close()

		body := fmt.Sprintf(`{"model": "gpt-5.6", "input": [
			{"type": "function_call_output", "call_id": "c1", "output": %q}
		]}`, longText)

		client := NewClient(ts.URL, "key", nil)
		if _, compressed, _ := client.Compress(context.Background(), []byte(body), "gpt-5.6", 1000); compressed {
			t.Error("a longer response must not be written back")
		}
	})
}

func readAll(r *http.Request) []byte {
	data, _ := io.ReadAll(r.Body)
	return data
}
