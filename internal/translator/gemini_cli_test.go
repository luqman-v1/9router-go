package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// Cloud Code Assist wraps the Gemini payload one level down, with the model
// beside it and no requestType/requestId/userId — the CLI envelope, not the
// antigravity one.
func TestWrapForGeminiCLINestsThePayload(t *testing.T) {
	gemini := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":64}}`)
	out, err := WrapForGeminiCLI(gemini, "proj-123", "gemini-cli/gemini-4-argon")
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}

	var envelope map[string]any
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("parse envelope: %v", err)
	}
	if envelope["project"] != "proj-123" {
		t.Errorf("project = %v, want proj-123", envelope["project"])
	}
	// The provider prefix is stripped: the backend keys on the bare id.
	if envelope["model"] != "gemini-4-argon" {
		t.Errorf("model = %v, want gemini-4-argon", envelope["model"])
	}
	inner, ok := envelope["request"].(map[string]any)
	if !ok {
		t.Fatalf("request is %T, want an object", envelope["request"])
	}
	if _, ok := inner["contents"]; !ok {
		t.Error("request lost the contents array")
	}
	// Antigravity's extra fields are what the CLI backend rejects.
	for _, banned := range []string{"userId", "requestType", "requestId", "userAgent"} {
		if _, present := envelope[banned]; present {
			t.Errorf("envelope carries %q, which belongs to the antigravity wrapper", banned)
		}
	}
}

// An already-wrapped body is left alone: nesting it twice is a malformed
// request, and a caller that passes the envelope through should not be punished.
func TestWrapForGeminiCLIIsIdempotent(t *testing.T) {
	envelope := []byte(`{"project":"p","model":"m","request":{"contents":[]}}`)
	out, err := WrapForGeminiCLI(envelope, "other", "other-model")
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if string(out) != string(envelope) {
		t.Errorf("an envelope was re-wrapped:\n got %s\nwant %s", out, envelope)
	}
}

func TestWrapForGeminiCLIRejectsAnEmptyBody(t *testing.T) {
	if _, err := WrapForGeminiCLI(nil, "p", "m"); err == nil {
		t.Error("expected an error for an empty body")
	}
}

// The UA and the envelope must name the same model or Google answers 403, so
// both go through the same normalisation.
func TestGeminiCLIModelAndUserAgentAgree(t *testing.T) {
	cases := map[string]string{
		"gemini-cli/gemini-4-argon":         "gemini-4-argon",
		"gc/gemini-4-argon":                 "gemini-4-argon",
		"gemini-4-argon-thinking":           "gemini-4-argon",
		"gemini-cli/gemini-3.1-pro-preview": "gemini-3.1-pro-preview",
		"gemini-cli/gemini-4-argon(high)":   "gemini-4-argon",
	}
	for in, want := range cases {
		if got := GeminiCLIModel(in); got != want {
			t.Errorf("GeminiCLIModel(%q) = %q, want %q", in, got, want)
		}
		ua := GeminiCLIUserAgent(in)
		if !strings.Contains(ua, want) {
			t.Errorf("GeminiCLIUserAgent(%q) = %q, which does not name %q", in, ua, want)
		}
	}
}
