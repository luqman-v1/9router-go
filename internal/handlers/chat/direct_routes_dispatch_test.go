package chat

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

// This file pins what can be pinned about the G06–G08 pre-pivot routes today.
//
// The first draft of this matrix described the three direct wire shapes but
// asserted on values it had built itself: the Responses item list, the Gemini
// functionResponse and the Kiro conversationState envelope were literals in
// this file marshalled and read straight back, and the dispatch table was a
// helper defined here with no production caller. Four of five subtests could
// not fail — mutating the Claude fixture's is_error to false left the suite
// green — which makes them a description of a wire shape rather than a test of
// one. The same defect class as the parallel store that was deleted from
// internal/translator.
//
// What remains asserts only through production symbols. Where the production
// symbol does not exist yet, the contract is a skip that names it: a skipped
// test has no pass value, while a vacuous one claims one.

// skipUntilProductionSeam records a contract that no production code can
// enforce yet, naming the seam it waits on.
func skipUntilProductionSeam(t *testing.T, seam, why string) {
	t.Helper()
	t.Skipf("not enforceable until %s exists: %s", seam, why)
}

// claudeFixture is a Claude Messages request carrying every property the
// direct routes exist to preserve: an is_error tool result, an inline image in
// that result, a signed thinking block, and a tool schema with $ref.
const claudeFixture = `{
	"model": "claude-3-7-sonnet",
	"system": "You are a helpful coding assistant.",
	"thinking": {
		"type": "enabled",
		"budget_tokens": 10000
	},
	"messages": [
		{
			"role": "user",
			"content": "Run calculation"
		},
		{
			"role": "assistant",
			"content": [
				{
					"type": "thinking",
					"thinking": "Calculating 1/0 which triggers division by zero.",
					"signature": "sig_thought_9router_123"
				},
				{
					"type": "tool_use",
					"id": "toolu_calc_01",
					"name": "divide",
					"input": {"dividend": 1, "divisor": 0}
				}
			]
		},
		{
			"role": "user",
			"content": [
				{
					"type": "tool_result",
					"tool_use_id": "toolu_calc_01",
					"is_error": true,
					"content": [
						{"type": "text", "text": "division by zero error"},
						{
							"type": "image",
							"source": {
								"type": "base64",
								"media_type": "image/png",
								"data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
							}
						}
					]
				}
			]
		}
	],
	"tools": [
		{
			"name": "divide",
			"description": "Divide two numbers",
			"input_schema": {
				"type": "object",
				"properties": {
					"dividend": {"type": "number"},
					"divisor": {"type": "number"}
				},
				"required": ["dividend", "divisor"],
				"$ref": "#/definitions/DivisionParams",
				"definitions": {
					"DivisionParams": {"type": "object"}
				}
			}
		}
	]
}`

// TestDirectRoutes_PivotLosesToolErrorState is the premise G06–G08 exist to
// remove, asserted against the converter that is actually in the request path.
// It is red the day the pivot starts preserving is_error, which is the signal
// that the direct routes have lost their reason to exist for that property.
func TestDirectRoutes_PivotLosesToolErrorState(t *testing.T) {
	openaiJSON, err := translator.TranslateClaudeToOpenAI([]byte(claudeFixture))
	if err != nil {
		t.Fatalf("TranslateClaudeToOpenAI: %v", err)
	}

	var oreq struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(openaiJSON, &oreq); err != nil {
		t.Fatalf("unmarshal translated body: %v", err)
	}

	var toolMsg map[string]any
	for _, m := range oreq.Messages {
		if m["role"] == "tool" {
			toolMsg = m
			break
		}
	}
	if toolMsg == nil {
		t.Fatalf("the pivot produced no role=tool message:\n%s", openaiJSON)
	}
	if toolMsg["tool_call_id"] != "toolu_calc_01" {
		t.Errorf("tool_call_id = %v, want toolu_calc_01: the pivot changed the id it must at least carry", toolMsg["tool_call_id"])
	}
	if _, has := toolMsg["is_error"]; has {
		t.Errorf("the pivot now preserves is_error; the direct Responses route no longer has that gap to close:\n%s", openaiJSON)
	}

	// The thinking signature is the other property the pivot cannot carry: the
	// Chat lane has one reasoning_content string and no field for a signature.
	if strings.Contains(string(openaiJSON), "sig_thought_9router_123") {
		t.Errorf("the pivot now preserves the thought signature:\n%s", openaiJSON)
	}
}

// TestDirectRoutes_ResponsesWirePreservesErrorState is where G06's direct
// wire contract lives once it exists. No converter turns a Claude body into a
// Responses body today — buildResponsesBody starts from Chat and would inherit
// the is_error loss asserted above.
func TestDirectRoutes_ResponsesWirePreservesErrorState(t *testing.T) {
	skipUntilProductionSeam(t, "translator.ClaudeToResponses",
		"the function_call_output item, its call_id, its is_error flag and the reasoning signature have no production path from a Claude body yet")
}

// TestDirectRoutes_GeminiSchemaSanitization is the one Gemini contract with a
// production seam today: CleanParametersSchema is what stands between a Claude
// tool schema and Gemini's 400 on an unsupported keyword. Red when the schema
// passes through untouched.
func TestDirectRoutes_GeminiSchemaSanitization(t *testing.T) {
	rawToolSchema := []byte(`{
		"type": "object",
		"properties": {
			"dividend": {"type": "number"},
			"divisor": {"type": "number"}
		},
		"required": ["dividend", "divisor"],
		"$ref": "#/definitions/DivisionParams",
		"definitions": {
			"DivisionParams": {"type": "object"}
		}
	}`)

	cleaned := translator.CleanParametersSchema(rawToolSchema)
	var cleanedMap map[string]any
	if err := json.Unmarshal(cleaned, &cleanedMap); err != nil {
		t.Fatalf("unmarshal cleaned schema: %v", err)
	}
	if _, has := cleanedMap["$ref"]; has {
		t.Errorf("$ref survived CleanParametersSchema:\n%s", cleaned)
	}
	if _, has := cleanedMap["definitions"]; has {
		t.Errorf("definitions survived CleanParametersSchema:\n%s", cleaned)
	}
	if _, has := cleanedMap["properties"]; !has {
		t.Errorf("CleanParametersSchema dropped the schema it was meant to keep:\n%s", cleaned)
	}
}

// TestDirectRoutes_GeminiWirePreservesToolError is where G07's direct wire
// contract lives once it exists.
func TestDirectRoutes_GeminiWirePreservesToolError(t *testing.T) {
	skipUntilProductionSeam(t, "translator.ClaudeToGemini",
		"systemInstruction placement and is_error inside functionResponse have no production path from a Claude body yet")
}

// TestDirectRoutes_KiroEventStreamDecoding drives the real decoder over a real
// frame, so a bounds, CRC or header-type regression in providers.ReadFrame is
// caught here rather than by a second implementation inside this file. Red when
// the decoder stops recovering the payload or the event-type header.
func TestDirectRoutes_KiroEventStreamDecoding(t *testing.T) {
	payload := []byte(`{"assistantResponseEvent":{"content":"Hello from Kiro"}}`)
	frame := buildEventStreamFrame(map[string]string{
		":event-type":   "assistantResponseEvent",
		":message-type": "event",
	}, payload)

	got, err := providers.NewEventStreamReader(bytes.NewReader(frame)).ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame on a well-formed frame: %v", err)
	}
	if got == nil {
		t.Fatal("ReadFrame returned no frame for a well-formed one")
	}
	if string(got.Payload) != string(payload) {
		t.Errorf("payload = %q, want %q", got.Payload, payload)
	}
	if got.Headers[":event-type"] != "assistantResponseEvent" {
		t.Errorf(":event-type header = %q, want assistantResponseEvent", got.Headers[":event-type"])
	}

	// A single flipped payload byte must be rejected by the message CRC, not
	// handed on as a silently corrupted response.
	corrupt := append([]byte(nil), frame...)
	corrupt[len(corrupt)-5] ^= 0xff
	if _, err := providers.NewEventStreamReader(bytes.NewReader(corrupt)).ReadFrame(); err == nil {
		t.Error("ReadFrame accepted a frame with a broken message CRC")
	}
}

// TestDirectRoutes_KiroEnvelopeShape is where G08's conversationState envelope
// contract lives once it exists.
func TestDirectRoutes_KiroEnvelopeShape(t *testing.T) {
	skipUntilProductionSeam(t, "translator.ClaudeToKiro",
		"the <instructions> wrapping, strict turn alternation and userInputMessageContext.toolResults status have no production path from a Claude body yet")
}

// TestDirectRoutes_DispatchMatrix is where the pre-pivot branch table lives once
// the branch point exists. HandleMessages currently pivots every non-Claude
// target to Chat Completions in one place (chat.go), with no per-target
// resolution to assert against.
func TestDirectRoutes_DispatchMatrix(t *testing.T) {
	skipUntilProductionSeam(t, "a pre-pivot route resolver in HandleMessages",
		"the dispatch table would be defined in the test that reads it, which is what the first draft of this file did")
}

// TestDirectRoutes_ComboLeavesShareAnImmutableBody pins the invariant every
// combo/account retry depends on: a converter may read the client body but must
// never write through it, because the next leaf receives the same slice. Red if
// any converter starts mutating its input.
func TestDirectRoutes_ComboLeavesShareAnImmutableBody(t *testing.T) {
	rawBody := []byte(claudeFixture)
	pristine := append([]byte(nil), rawBody...)

	// The leaves a combo would walk, in the order they would be tried.
	for _, target := range []string{"chat-pivot", "chat-pivot"} {
		if _, err := translator.TranslateClaudeToOpenAI(rawBody); err != nil {
			t.Fatalf("%s: TranslateClaudeToOpenAI: %v", target, err)
		}
		if !bytes.Equal(rawBody, pristine) {
			t.Fatalf("%s mutated the client body; the next combo leaf would receive a converted payload", target)
		}
	}
}
