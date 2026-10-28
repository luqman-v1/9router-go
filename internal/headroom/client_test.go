package headroom

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHeadroomClient_CompressContract(t *testing.T) {
	t.Run("SuccessfulCompression", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/compress" {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"messages": [
					{"role": "user", "content": "Short compressed query"}
				],
				"tokens_before": 100,
				"tokens_after": 20,
				"tokens_saved": 80
			}`))
		}))
		defer ts.Close()

		client := NewClient(ts.URL, "test-api-key", nil)
		origBody := []byte(`{
			"model": "gpt-4o",
			"messages": [
				{"role": "user", "content": "A very very long query that should be compressed by Headroom proxy..."}
			]
		}`)

		resBody, compressed, err := client.Compress(context.Background(), origBody, "gpt-4o", 1000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !compressed {
			t.Fatalf("expected compressed=true, got false")
		}

		var parsed map[string]any
		if err := json.Unmarshal(resBody, &parsed); err != nil {
			t.Fatalf("failed to parse returned body: %v", err)
		}
		msgs := parsed["messages"].([]any)
		msg0 := msgs[0].(map[string]any)
		if msg0["content"] != "Short compressed query" {
			t.Fatalf("unexpected content: %v", msg0["content"])
		}
	})

	t.Run("StructureTamperingRejected", func(t *testing.T) {
		t.Run("ToolCallsDropped", func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				// Tampering: drops tool_calls completely
				w.Write([]byte(`{
					"messages": [
						{"role": "user", "content": "compressed user"},
						{"role": "assistant", "content": "compressed assistant"}
					],
					"tokens_saved": 50
				}`))
			}))
			defer ts.Close()

			client := NewClient(ts.URL, "", nil)
			origBody := []byte(`{
				"model": "gpt-4o",
				"messages": [
					{"role": "user", "content": "long user message"},
					{
						"role": "assistant",
						"content": "long assistant message",
						"tool_calls": [
							{
								"id": "call_abc123",
								"type": "function",
								"function": {
									"name": "fetch_data",
									"arguments": "{\"query\":\"test\"}"
								}
							}
						]
					}
				]
			}`)

			resBody, compressed, err := client.Compress(context.Background(), origBody, "gpt-4o", 1000)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if compressed {
				t.Fatalf("expected compressed=false on tool_calls dropped")
			}
			if !bytes.Equal(resBody, origBody) {
				t.Fatalf("expected original body returned on structure tampering")
			}
		})

		t.Run("RolesSwapped", func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				// Tampering: swapped roles
				w.Write([]byte(`{
					"messages": [
						{"role": "assistant", "content": "answer"},
						{"role": "user", "content": "question"}
					],
					"tokens_saved": 50
				}`))
			}))
			defer ts.Close()

			client := NewClient(ts.URL, "", nil)
			origBody := []byte(`{
				"messages": [
					{"role": "user", "content": "question"},
					{"role": "assistant", "content": "answer"}
				]
			}`)

			resBody, compressed, err := client.Compress(context.Background(), origBody, "gpt-4o", 1000)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if compressed {
				t.Fatalf("expected compressed=false on roles swapped")
			}
			if !bytes.Equal(resBody, origBody) {
				t.Fatalf("expected original body returned on role swap")
			}
		})

		t.Run("MessageCountChanged", func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				// Tampering: reduced to 1 message
				w.Write([]byte(`{
					"messages": [
						{"role": "user", "content": "only one"}
					],
					"tokens_saved": 50
				}`))
			}))
			defer ts.Close()

			client := NewClient(ts.URL, "", nil)
			origBody := []byte(`{
				"messages": [
					{"role": "user", "content": "msg 1"},
					{"role": "assistant", "content": "msg 2"}
				]
			}`)

			resBody, compressed, err := client.Compress(context.Background(), origBody, "gpt-4o", 1000)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if compressed {
				t.Fatalf("expected compressed=false on message count mismatch")
			}
			if !bytes.Equal(resBody, origBody) {
				t.Fatalf("expected original body returned on message count change")
			}
		})

		t.Run("ToolCallArgumentsTampered", func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				// Tampering: changed arguments from main.go to exploit.go
				w.Write([]byte(`{
					"messages": [
						{
							"role": "assistant",
							"tool_calls": [
								{
									"id": "call_1",
									"type": "function",
									"function": {
										"name": "read_file",
										"arguments": "{\"path\":\"exploit.go\"}"
									}
								}
							]
						}
					],
					"tokens_saved": 10
				}`))
			}))
			defer ts.Close()

			client := NewClient(ts.URL, "", nil)
			origBody := []byte(`{
				"messages": [
					{
						"role": "assistant",
						"tool_calls": [
							{
								"id": "call_1",
								"type": "function",
								"function": {
									"name": "read_file",
									"arguments": "{\"path\":\"main.go\"}"
								}
							}
						]
					}
				]
			}`)

			resBody, compressed, err := client.Compress(context.Background(), origBody, "gpt-4o", 1000)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if compressed {
				t.Fatalf("expected compressed=false on tool call argument tampering")
			}
			if !bytes.Equal(resBody, origBody) {
				t.Fatalf("expected original body returned on argument tampering")
			}
		})
	})

	t.Run("CCRHashesRejected", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"messages": [
					{"role": "user", "content": "compressed text"}
				],
				"tokens_saved": 40,
				"ccr_hashes": ["sha256-blob-12345"]
			}`))
		}))
		defer ts.Close()

		client := NewClient(ts.URL, "", nil)
		origBody := []byte(`{
			"messages": [
				{"role": "user", "content": "original text"}
			]
		}`)

		resBody, compressed, err := client.Compress(context.Background(), origBody, "gpt-4o", 1000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if compressed {
			t.Fatalf("expected compressed=false when ccr_hashes are present")
		}
		if !bytes.Equal(resBody, origBody) {
			t.Fatalf("expected original body returned on ccr_hashes")
		}
	})

	t.Run("CCRMarkerRejected", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"messages": [
					{"role": "user", "content": "hello <<ccr:blob-xyz>> world"}
				],
				"tokens_saved": 40
			}`))
		}))
		defer ts.Close()

		client := NewClient(ts.URL, "", nil)
		origBody := []byte(`{
			"messages": [
				{"role": "user", "content": "original text"}
			]
		}`)

		resBody, compressed, err := client.Compress(context.Background(), origBody, "gpt-4o", 1000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if compressed {
			t.Fatalf("expected compressed=false when <<ccr: marker is present")
		}
		if !bytes.Equal(resBody, origBody) {
			t.Fatalf("expected original body returned on <<ccr: marker")
		}
	})

	t.Run("CompressionSkipped", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"messages": [
					{"role": "user", "content": "original text"}
				],
				"compression_skipped": true,
				"skip_reason": "already compact"
			}`))
		}))
		defer ts.Close()

		client := NewClient(ts.URL, "", nil)
		origBody := []byte(`{
			"messages": [
				{"role": "user", "content": "original text"}
			]
		}`)

		resBody, compressed, err := client.Compress(context.Background(), origBody, "gpt-4o", 1000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if compressed {
			t.Fatalf("expected compressed=false when compression_skipped=true")
		}
		if !bytes.Equal(resBody, origBody) {
			t.Fatalf("expected original body returned on compression_skipped")
		}
	})

	t.Run("TimeoutFailOpen", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"messages": [{"role": "user", "content": "late"}]}`))
		}))
		defer ts.Close()

		client := NewClient(ts.URL, "", nil)
		origBody := []byte(`{
			"messages": [
				{"role": "user", "content": "original query"}
			]
		}`)

		// Timeout set to 50ms, server sleeps 200ms
		resBody, compressed, err := client.Compress(context.Background(), origBody, "gpt-4o", 50)
		if err != nil {
			t.Fatalf("expected nil error on fail-open timeout, got: %v", err)
		}
		if compressed {
			t.Fatalf("expected compressed=false on timeout fail-open")
		}
		if !bytes.Equal(resBody, origBody) {
			t.Fatalf("expected original body on timeout fail-open")
		}
	})

	t.Run("NetworkErrorFailOpen", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
				return
			}
			http.Error(w, "error", http.StatusInternalServerError)
		}))
		defer ts.Close()

		client := NewClient(ts.URL, "", nil)
		origBody := []byte(`{
			"messages": [
				{"role": "user", "content": "original query"}
			]
		}`)

		resBody, compressed, err := client.Compress(context.Background(), origBody, "gpt-4o", 1000)
		if err != nil {
			t.Fatalf("expected nil error on fail-open network error, got: %v", err)
		}
		if compressed {
			t.Fatalf("expected compressed=false on network error fail-open")
		}
		if !bytes.Equal(resBody, origBody) {
			t.Fatalf("expected original body on network error fail-open")
		}
	})

	t.Run("DownstreamCancelStopsDispatch", func(t *testing.T) {
		var dispatched atomic.Bool
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			dispatched.Store(true)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"messages": [{"role": "user", "content": "ok"}]}`))
		}))
		defer ts.Close()

		client := NewClient(ts.URL, "", nil)
		origBody := []byte(`{
			"messages": [
				{"role": "user", "content": "original query"}
			]
		}`)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel before calling

		resBody, compressed, err := client.Compress(ctx, origBody, "gpt-4o", 1000)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled error, got: %v", err)
		}
		if compressed {
			t.Fatalf("expected compressed=false on canceled context")
		}
		if resBody != nil {
			t.Fatalf("expected nil body on canceled downstream context")
		}
		if dispatched.Load() {
			t.Fatalf("expected no dispatch to compressor when downstream is already canceled")
		}
	})

	t.Run("ThinkingAndImagePreservation", func(t *testing.T) {
		const imageData = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkWPjfDwAExwH0j7vA8AAAAABJRU5ErkJggg=="
		const thinkingSignature = "sig_thought_999"
		const thinkingText = "Thinking process block"
		const reasoningContent = "Chain-of-thought that must not be altered"

		origBody := []byte(`{
			"model": "claude-3-7-sonnet",
			"messages": [
				{
					"role": "user",
					"content": [
						{"type": "text", "text": "Very long user question that should be shortened by compressor"},
						{"type": "image_url", "image_url": {"url": "` + imageData + `"}}
					]
				},
				{
					"role": "assistant",
					"reasoning_content": "` + reasoningContent + `",
					"content": [
						{"type": "thinking", "thinking": "` + thinkingText + `", "signature": "` + thinkingSignature + `"},
						{"type": "text", "text": "Long assistant answer to compress"}
					]
				}
			]
		}`)

		t.Run("PreservedSuccessfully", func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{
					"messages": [
						{
							"role": "user",
							"content": [
								{"type": "text", "text": "Short user question"},
								{"type": "image_url", "image_url": {"url": "` + imageData + `"}}
							]
						},
						{
							"role": "assistant",
							"reasoning_content": "` + reasoningContent + `",
							"content": [
								{"type": "thinking", "thinking": "` + thinkingText + `", "signature": "` + thinkingSignature + `"},
								{"type": "text", "text": "Short answer"}
							]
						}
					],
					"tokens_saved": 120
				}`))
			}))
			defer ts.Close()

			client := NewClient(ts.URL, "", nil)
			resBody, compressed, err := client.Compress(context.Background(), origBody, "claude-3-7-sonnet", 1000)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !compressed {
				t.Fatalf("expected compressed=true")
			}

			var parsed map[string]any
			if err := json.Unmarshal(resBody, &parsed); err != nil {
				t.Fatalf("failed to parse returned body: %v", err)
			}
			msgs := parsed["messages"].([]any)
			userMsg := msgs[0].(map[string]any)
			userContent := userMsg["content"].([]any)
			uTxt := userContent[0].(map[string]any)
			if uTxt["text"] != "Short user question" {
				t.Fatalf("unexpected user text: %v", uTxt["text"])
			}
			uImg := userContent[1].(map[string]any)
			imgUrl := uImg["image_url"].(map[string]any)["url"]
			if imgUrl != imageData {
				t.Fatalf("image data corrupted: %v", imgUrl)
			}

			asstMsg := msgs[1].(map[string]any)
			if asstMsg["reasoning_content"] != reasoningContent {
				t.Fatalf("reasoning_content corrupted: %v", asstMsg["reasoning_content"])
			}
			asstContent := asstMsg["content"].([]any)
			asstThinking := asstContent[0].(map[string]any)
			if asstThinking["thinking"] != thinkingText || asstThinking["signature"] != thinkingSignature {
				t.Fatalf("thinking block corrupted: %v", asstThinking)
			}
			asstTxt := asstContent[1].(map[string]any)
			if asstTxt["text"] != "Short answer" {
				t.Fatalf("unexpected asst text: %v", asstTxt["text"])
			}
		})

		t.Run("ThinkingTamperedRejected", func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				// Tampering: altered thinking signature
				w.Write([]byte(`{
					"messages": [
						{
							"role": "user",
							"content": [
								{"type": "text", "text": "Short user question"},
								{"type": "image_url", "image_url": {"url": "` + imageData + `"}}
							]
						},
						{
							"role": "assistant",
							"reasoning_content": "` + reasoningContent + `",
							"content": [
								{"type": "thinking", "thinking": "` + thinkingText + `", "signature": "sig_tampered_bad"},
								{"type": "text", "text": "Short answer"}
							]
						}
					],
					"tokens_saved": 120
				}`))
			}))
			defer ts.Close()

			client := NewClient(ts.URL, "", nil)
			resBody, compressed, err := client.Compress(context.Background(), origBody, "claude-3-7-sonnet", 1000)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if compressed {
				t.Fatalf("expected compressed=false when thinking signature is tampered")
			}
			if !bytes.Equal(resBody, origBody) {
				t.Fatalf("expected original body returned on thinking tampering")
			}
		})

		t.Run("ImageTamperedRejected", func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				// Tampering: altered image url
				w.Write([]byte(`{
					"messages": [
						{
							"role": "user",
							"content": [
								{"type": "text", "text": "Short user question"},
								{"type": "image_url", "image_url": {"url": "data:image/png;base64,TAMPERED"}}
							]
						},
						{
							"role": "assistant",
							"reasoning_content": "` + reasoningContent + `",
							"content": [
								{"type": "thinking", "thinking": "` + thinkingText + `", "signature": "` + thinkingSignature + `"},
								{"type": "text", "text": "Short answer"}
							]
						}
					],
					"tokens_saved": 120
				}`))
			}))
			defer ts.Close()

			client := NewClient(ts.URL, "", nil)
			resBody, compressed, err := client.Compress(context.Background(), origBody, "claude-3-7-sonnet", 1000)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if compressed {
				t.Fatalf("expected compressed=false when image data is tampered")
			}
			if !bytes.Equal(resBody, origBody) {
				t.Fatalf("expected original body returned on image tampering")
			}
		})
	})
}

// --- Skip guards -----------------------------------------------------------
//
// The proxy these guards talk to is a separate process that a caller may or may
// not have running. Every assertion below is written as "the request never left
// this process": a tripwire server fails the test if it is reached, so a guard
// that silently stopped working shows up as a request, not as a missing log line.

func tripwireServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[{"role":"user","content":"x"}],"tokens_saved":50}`))
	}))
	t.Cleanup(ts.Close)
	return ts, &hits
}

func TestCompress_SkipsErrorToolResult(t *testing.T) {
	cases := map[string]string{
		"chat is_error": `{"model":"m","messages":[
			{"role":"tool","tool_call_id":"c1","is_error":true,"content":"boom"}]}`,
		"chat status error": `{"model":"m","messages":[
			{"role":"tool","tool_call_id":"c1","status":"error","content":"boom"}]}`,
		"claude block is_error": `{"model":"m","messages":[
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"boom"}]}]}`,
		// A non-empty messages array, so the early return on an empty body cannot
		// make this case pass without the guard.
		"responses function_call_output": `{"model":"m","messages":[
			{"role":"user","content":"hello"}],"input":[
			{"type":"function_call_output","call_id":"c1","status":"error","output":"boom"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			ts, hits := tripwireServer(t)
			client := NewClient(ts.URL, "", nil)
			got, compressed, err := client.Compress(context.Background(), []byte(body), "m", 1000)
			if err != nil {
				t.Fatalf("guard must fail open, got err: %v", err)
			}
			if compressed {
				t.Fatalf("an error tool result must not be compressed")
			}
			if string(got) != body {
				t.Fatalf("body changed by a skipped request")
			}
			if n := atomic.LoadInt32(hits); n != 0 {
				t.Fatalf("proxy was called %d times; the guard did not stop it", n)
			}
		})
	}
}

func TestCompress_SkipsProtectedTool(t *testing.T) {
	cases := map[string]string{
		"chat protected": `{"model":"m","messages":[
			{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"Read","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"c1","content":"file contents"}]}`,
		"claude block protected": `{"model":"m","messages":[
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","name":"Edit","content":"x"}]}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			ts, hits := tripwireServer(t)
			client := NewClient(ts.URL, "", nil)
			_, compressed, err := client.Compress(context.Background(), []byte(body), "m", 1000)
			if err != nil {
				t.Fatalf("guard must fail open, got err: %v", err)
			}
			if compressed {
				t.Fatalf("a protected tool's output must not be compressed")
			}
			if n := atomic.LoadInt32(hits); n != 0 {
				t.Fatalf("proxy was called %d times; the guard did not stop it", n)
			}
		})
	}
}

func TestCompress_SkipsUnknownTool(t *testing.T) {
	// A tool name the gateway has never heard of is not a known command tool, so
	// its output is not something a lossy compressor has been shown to be safe on.
	body := `{"model":"m","messages":[
		{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"some_mcp_tool","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"https://example.test/whatever"}]}`
	ts, hits := tripwireServer(t)
	client := NewClient(ts.URL, "", nil)
	_, compressed, err := client.Compress(context.Background(), []byte(body), "m", 1000)
	if err != nil {
		t.Fatalf("guard must fail open, got err: %v", err)
	}
	if compressed {
		t.Fatalf("an unknown tool's output must not be compressed")
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Fatalf("proxy was called %d times; the guard did not stop it", n)
	}
}

func TestCompress_SkipsUnknownToolWithNoName(t *testing.T) {
	// A tool result whose call id resolves to nothing is the same hazard as an
	// unknown name: `isUnknownTool("")` is true in the reference implementation.
	body := `{"model":"m","messages":[
		{"role":"tool","tool_call_id":"dangling","content":"orphan"}]}`
	ts, hits := tripwireServer(t)
	client := NewClient(ts.URL, "", nil)
	_, compressed, _ := client.Compress(context.Background(), []byte(body), "m", 1000)
	if compressed {
		t.Fatalf("a tool result with no resolvable name must not be compressed")
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Fatalf("proxy was called %d times; the guard did not stop it", n)
	}
}

func TestCompress_AllowsKnownCommandTool(t *testing.T) {
	// The mirror of the test above: bash output IS a known command tool, so the
	// guard must let it through. Without this, a guard that skipped everything
	// would pass every other test in this file.
	body := `{"model":"m","messages":[
		{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"bash","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"a long log line that compresses"}]}`
	ts, hits := tripwireServer(t)
	client := NewClient(ts.URL, "", nil)
	_, _, err := client.Compress(context.Background(), []byte(body), "m", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := atomic.LoadInt32(hits); n == 0 {
		t.Fatalf("a known command tool must reach the proxy, but it was never called")
	}
}

// TestCompress_ProtectsToolNamesInAnyCase pins the case folding. The same tool
// reaches the gateway spelled "Read" by one client and "read" by another, so a
// case-sensitive set protects only the clients that happen to capitalise — and
// that is exactly how this list and tokensaver's protectedTools diverged.
func TestCompress_ProtectsToolNamesInAnyCase(t *testing.T) {
	for _, name := range []string{"Read", "read", "READ", "  read  ", "Grep", "grep", "Edit", "edit", "Write", "write"} {
		t.Run(name, func(t *testing.T) {
			body := fmt.Sprintf(`{"model":"m","messages":[
				{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":%q,"arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"c1","content":"a long file body that compresses"}]}`, name)
			ts, hits := tripwireServer(t)
			client := NewClient(ts.URL, "", nil)
			_, compressed, err := client.Compress(context.Background(), []byte(body), "m", 1000)
			if err != nil {
				t.Fatalf("guard must fail open, got err: %v", err)
			}
			if compressed {
				t.Fatalf("tool %q must be protected regardless of case", name)
			}
			if n := atomic.LoadInt32(hits); n != 0 {
				t.Fatalf("proxy was called %d times for %q; the guard did not stop it", n, name)
			}
		})
	}
}

func TestCompress_RejectsEmptiedTextBlock(t *testing.T) {
	// A block that had text and comes back empty is not a compression, it is a
	// deletion. The reference implementation catches this by comparing sizes
	// after the round trip; the structural validator only checks that `text` is
	// present, so "" would otherwise pass.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[
			{"role":"user","content":[{"type":"text","text":""}]}],
			"tokens_before":100,"tokens_after":1,"tokens_saved":99}`))
	}))
	defer ts.Close()

	body := `{"model":"m","messages":[
		{"role":"user","content":[{"type":"text","text":"a paragraph that has real content"}]}]}`
	client := NewClient(ts.URL, "", nil)
	got, compressed, err := client.Compress(context.Background(), []byte(body), "m", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if compressed {
		t.Fatalf("a block emptied by the round trip must be rejected")
	}
	if string(got) != body {
		t.Fatalf("original body was not preserved")
	}
}

func TestCompress_RejectsPhantomSaving(t *testing.T) {
	// The proxy reports a large token saving but hands back almost the same
	// bytes. Committing that trades a real risk for no measurable benefit, so
	// the body is kept unless the result is meaningfully smaller.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Same message, different whitespace only: round-trips to ~the same size.
		_, _ = w.Write([]byte(`{"messages":[
			{"role":"user","content":"a paragraph that has real content and is not shorter at all"}],
			"tokens_before":1000,"tokens_after":10,"tokens_saved":990}`))
	}))
	defer ts.Close()

	body := `{"model":"m","messages":[
		{"role":"user","content":"a paragraph that has real content and is not shorter at all"}]}`
	client := NewClient(ts.URL, "", nil)
	_, compressed, err := client.Compress(context.Background(), []byte(body), "m", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if compressed {
		t.Fatalf("a result that does not shrink the body must not be committed")
	}
}

func TestCompress_AcceptsRealSaving(t *testing.T) {
	// The mirror: a genuinely smaller body IS committed. Without this a guard
	// that rejected everything would pass the two tests above.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[
			{"role":"user","content":"short"}],
			"tokens_before":1000,"tokens_after":5,"tokens_saved":995}`))
	}))
	defer ts.Close()

	body := `{"model":"m","messages":[
		{"role":"user","content":"a very long paragraph that the compressor can genuinely shorten by a lot"}]}`
	client := NewClient(ts.URL, "", nil)
	got, compressed, err := client.Compress(context.Background(), []byte(body), "m", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !compressed {
		t.Fatalf("a genuinely smaller body must be committed")
	}
	if string(got) == body {
		t.Fatalf("body was not replaced")
	}
}
