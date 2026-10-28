//go:build ignore

package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMockStreamComesFromJSONBody(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		want    bool
		wantErr bool
	}{
		{"stream", `{"stream":true}`, true, false},
		{"json", `{"stream":false}`, false, false},
		{"malformed", `{`, false, true},
		{"trailing", `{"stream":true}{}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tc.body))
			got, err := mockWantsStream(httptest.NewRecorder(), request)
			if (err != nil) != tc.wantErr || (!tc.wantErr && got != tc.want) {
				t.Fatalf("got=%v err=%v", got, err)
			}
		})
	}
}

// TestMockChatReflectsTools pins that a turn asking for a tool gets a tool
// call back. A mock that always answers with prose cannot measure the tool
// path, and the tool path is the one the matrix has to cover.
func TestMockChatReflectsTools(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		stream bool
	}{
		{"stream", `{"model":"gpt-4o","stream":true,"tools":[{"type":"function","function":{"name":"mock_tool"}}]}`, true},
		{"json", `{"model":"gpt-4o","stream":false,"tools":[{"type":"function","function":{"name":"mock_tool"}}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			features := detectMockFeatures([]byte(tc.body))
			if !features.HasTools {
				t.Fatal("tools in the request were not detected")
			}
		})
	}
}

// TestMockChatReflectsReasoning pins the reasoning trigger: an explicit effort,
// an explicit flag, or a model id that names reasoning.
func TestMockChatReflectsReasoning(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"effort", `{"model":"gpt-4o","reasoning_effort":"medium"}`, true},
		{"flag", `{"model":"gpt-4o","include_reasoning":true}`, true},
		{"model", `{"model":"gpt-4o-reasoning"}`, true},
		{"none", `{"model":"gpt-4o"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			features := detectMockFeatures([]byte(tc.body))
			if features.Reasoning != tc.want {
				t.Fatalf("reasoning=%v want=%v", features.Reasoning, tc.want)
			}
		})
	}
}

// TestMockDetectFeaturesToleratesGarbage keeps a body the mock cannot parse
// from taking the mock down: the gateway is the layer that reports a bad
// request, and the mock must still answer.
func TestMockDetectFeaturesToleratesGarbage(t *testing.T) {
	features := detectMockFeatures([]byte(`{not json`))
	if features.HasTools || features.Reasoning {
		t.Fatalf("garbage body produced features: %+v", features)
	}
}
