package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestThoughtSignatureStore_BasicAndSession(t *testing.T) {
	ClearGeminiThoughtSignatures()

	callID := "call_test_123"
	sig := "sig_abc_xyz"
	sessionID := "session_1"

	StoreGeminiThoughtSignature(callID, sig, sessionID)

	// Retrieve by session + callID
	if got := GetGeminiThoughtSignature(callID, sessionID); got != sig {
		t.Errorf("expected %q, got %q", sig, got)
	}

	// Retrieve by callID alone (global fallback)
	if got := GetGeminiThoughtSignature(callID, ""); got != sig {
		t.Errorf("expected %q, got %q", sig, got)
	}

	// Retrieve with __ts__ suffix attached
	if got := GetGeminiThoughtSignature(callID+"__ts__dummy", sessionID); got != sig {
		t.Errorf("expected %q for clean lookup, got %q", sig, got)
	}

	// Unknown callID
	if got := GetGeminiThoughtSignature("call_unknown", sessionID); got != "" {
		t.Errorf("expected empty for unknown, got %q", got)
	}
}

func TestThoughtSignatureStore_ParallelCallsFirstGetsSig(t *testing.T) {
	ClearGeminiThoughtSignatures()

	reqJSON := []byte(`{
		"model": "gemini-2.5-flash",
		"messages": [
			{
				"role": "assistant",
				"tool_calls": [
					{
						"id": "call_first_001",
						"type": "function",
						"function": {
							"name": "test_tool_1",
							"arguments": "{\"a\":1}"
						}
					},
					{
						"id": "call_second_002",
						"type": "function",
						"function": {
							"name": "test_tool_2",
							"arguments": "{\"b\":2}"
						}
					}
				]
			}
		]
	}`)

	geminiBytes, err := TranslateOpenAIToGemini(reqJSON)
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
	}

	var gReq GeminiRequest
	if err := json.Unmarshal(geminiBytes, &gReq); err != nil {
		t.Fatalf("unmarshal geminiBytes failed: %v", err)
	}

	// NormalizeGeminiContents brackets the conversation with user turns, so the
	// calls are located by role instead of by a fixed index.
	var calls []GeminiPart
	for _, c := range gReq.Contents {
		if c.Role != "model" {
			continue
		}
		calls = append(calls, c.Parts...)
	}
	parts := calls
	if len(parts) != 2 {
		t.Fatalf("expected 2 function-call parts, got %d (contents: %d)", len(parts), len(gReq.Contents))
	}

	// First call should get DefaultThinkingSignature
	if parts[0].ThoughtSignature != DefaultThinkingSignature {
		t.Errorf("first call should have default signature, got %q", parts[0].ThoughtSignature)
	}

	// Second sibling call without cached sig should be unsigned (empty)
	if parts[1].ThoughtSignature != "" {
		t.Errorf("second sibling call should be unsigned, got %q", parts[1].ThoughtSignature)
	}
}

func TestThoughtSignatureStore_FamilyScoping(t *testing.T) {
	ClearGeminiThoughtSignatures()

	claudeCall := "toolu_123"
	geminiCall := "call_456"
	storeSession := "sess"

	StoreGeminiThoughtSignature(claudeCall, "CLAUDE_SIG", storeSession, "claude-opus-4-6-thinking")
	StoreGeminiThoughtSignature(geminiCall, "GEMINI_SIG", storeSession, "gemini-3.8-flash-tiered")

	// Cross-family lookups must be rejected
	if got := GetGeminiThoughtSignature(claudeCall, storeSession, "gemini-3.8-flash"); got != "" {
		t.Errorf("expected empty signature when querying Claude sig for Gemini target, got %q", got)
	}
	if got := GetGeminiThoughtSignature(geminiCall, storeSession, "claude-sonnet-4-6"); got != "" {
		t.Errorf("expected empty signature when querying Gemini sig for Claude target, got %q", got)
	}

	// Same family lookups must succeed
	if got := GetGeminiThoughtSignature(claudeCall, storeSession, "claude-sonnet-4-6"); got != "CLAUDE_SIG" {
		t.Errorf("expected CLAUDE_SIG for Claude family query, got %q", got)
	}
	if got := GetGeminiThoughtSignature(geminiCall, storeSession, "gemini-2.5-pro"); got != "GEMINI_SIG" {
		t.Errorf("expected GEMINI_SIG for Gemini family query, got %q", got)
	}

	// Untagged queries/entries remain backward compatible
	untaggedCall := "call_legacy"
	StoreGeminiThoughtSignature(untaggedCall, "LEGACY_SIG", storeSession)
	if got := GetGeminiThoughtSignature(untaggedCall, storeSession, "gemini-3.8-flash"); got != "LEGACY_SIG" {
		t.Errorf("expected LEGACY_SIG for untagged entry, got %q", got)
	}
}

func TestNormalizeGeminiThoughtSignature(t *testing.T) {
	// Standard valid signature
	if got := NormalizeGeminiThoughtSignature(DefaultThinkingSignature); got != DefaultThinkingSignature {
		t.Errorf("NormalizeGeminiThoughtSignature(DefaultThinkingSignature) = %q, want %q", got, DefaultThinkingSignature)
	}

	// Double-base64 signature (starts with Q0FR...) falls back to DefaultThinkingSignature
	doubleB64 := "Q0FRUzI3TUJDaElJRXhBQ0dBSTRBVUlJZEdocGJt"
	if got := NormalizeGeminiThoughtSignature(doubleB64); got != DefaultThinkingSignature {
		t.Errorf("expected fallback to DefaultThinkingSignature for Q0FR signature, got %q", got)
	}

	// Oversized signature (> 32KB) falls back to DefaultThinkingSignature
	oversized := strings.Repeat("A", 33*1024)
	if got := NormalizeGeminiThoughtSignature(oversized); got != DefaultThinkingSignature {
		t.Errorf("expected fallback to DefaultThinkingSignature for oversized signature, got %q", got)
	}

	if got := NormalizeGeminiThoughtSignature(""); got != "" {
		t.Errorf("expected empty string for empty input, got %q", got)
	}
}

func TestPurgeSessionThoughtSignatures(t *testing.T) {
	sess := "test-session-purge"
	callID := "call_purge_1"
	StoreGeminiThoughtSignature(callID, DefaultThinkingSignature, sess, "gemini-4-argon")

	if got := GetGeminiThoughtSignature(callID, sess, "gemini-4-argon"); got == "" {
		t.Fatal("expected signature to be stored")
	}

	PurgeSessionThoughtSignatures(sess)

	if got := GetGeminiThoughtSignature(callID, sess, "gemini-4-argon"); got != "" {
		t.Errorf("expected signature to be purged, got %q", got)
	}
}
