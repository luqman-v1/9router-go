package executor

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStripClientMetadata_RemovesFromResponseBody(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantMeta bool // whether client_metadata should remain
		wantKeys []string
		wantOmit []string
	}{
		{
			name:     "strips top-level client_metadata",
			body:     `{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"client_metadata":{"foo":"bar"}}`,
			wantKeys: []string{"id", "object", "status", "output"},
			wantOmit: []string{"client_metadata"},
		},
		{
			name:     "leaves body without client_metadata untouched",
			body:     `{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`,
			wantKeys: []string{"id", "object", "status", "output"},
			wantMeta: false,
		},
		{
			name: "invalid json is returned unchanged",
			body: `not-json`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripClientMetadata([]byte(tt.body))
			if len(tt.wantKeys) == 0 && len(tt.wantOmit) == 0 {
				// invalid json / passthrough case: unchanged
				if string(got) != tt.body {
					t.Errorf("stripClientMetadata(%q) = %q, want unchanged %q", tt.body, string(got), tt.body)
				}
				return
			}
			var m map[string]any
			if err := json.Unmarshal(got, &m); err != nil {
				t.Fatalf("stripClientMetadata produced unparseable JSON %q: %v", string(got), err)
			}
			for _, k := range tt.wantKeys {
				if _, ok := m[k]; !ok {
					t.Errorf("result missing key %q; got %v", k, m)
				}
			}
			for _, k := range tt.wantOmit {
				if _, ok := m[k]; ok {
					t.Errorf("result still has key %q; got %v", k, m)
				}
			}
			_, hadMeta := m["client_metadata"]
			if hadMeta != tt.wantMeta {
				t.Errorf("client_metadata present = %v, want %v", hadMeta, tt.wantMeta)
			}
		})
	}
}

func TestPassthroughResponses_StripsClientMetadata(t *testing.T) {
	body := `{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"client_metadata":{"foo":"bar"}}`
	upstream := strings.NewReader(body)
	req := &Request{Ctx: nil, IsStream: false}

	rec := httptest.NewRecorder()
	if err := passthroughResponses(rec, req, upstream); err != nil {
		t.Fatalf("passthroughResponses: %v", err)
	}

	out := rec.Body.String()
	if strings.Contains(out, "client_metadata") {
		t.Errorf("client_metadata leaked into the response:\n%s", out)
	}
	if !strings.Contains(out, `"status":"completed"`) {
		t.Errorf("response body was altered beyond stripping client_metadata:\n%s", out)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestPassthroughResponses_LeavesMetadataFreeBodyUntouched(t *testing.T) {
	body := `{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`
	upstream := strings.NewReader(body)
	req := &Request{Ctx: nil, IsStream: false}

	rec := httptest.NewRecorder()
	if err := passthroughResponses(rec, req, upstream); err != nil {
		t.Fatalf("passthroughResponses: %v", err)
	}

	out := rec.Body.String()
	if strings.Contains(out, "client_metadata") {
		t.Errorf("client_metadata appeared in a clean response:\n%s", out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("passthroughResponses produced unparseable JSON %q: %v", out, err)
	}
	for _, k := range []string{"id", "object", "status", "output"} {
		if _, ok := m[k]; !ok {
			t.Errorf("result missing key %q; got %v", k, m)
		}
	}
}
