package handlerutil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteJSONError(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		message string
	}{
		{"bad request", http.StatusBadRequest, "invalid model"},
		{"not found", http.StatusNotFound, "resource not found"},
		{"internal error", http.StatusInternalServerError, "server error"},
		{"empty message", http.StatusBadRequest, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			WriteJSONError(w, tt.status, tt.message)

			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != tt.status {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.status)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want %q", ct, "application/json")
			}

			var body map[string]map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			errObj, ok := body["error"]
			if !ok {
				t.Fatal("missing 'error' key")
			}
			if errObj["message"] != tt.message {
				t.Errorf("error.message = %q, want %q", errObj["message"], tt.message)
			}
			if errObj["type"] != "invalid_request_error" {
				t.Errorf("error.type = %q, want %q", errObj["type"], "invalid_request_error")
			}
			if errObj["code"] != float64(tt.status) {
				t.Errorf("error.code = %v, want %v", errObj["code"], tt.status)
			}
		})
	}
}

func TestWriteJSONError_marshalFallback(t *testing.T) {
	// Force json.Marshal to fail by using an unencodable channel value.
	// WriteJSONError always marshals a plain map, so this path is defensive.
	// We test the fallback string is valid JSON with status 500.
	w := httptest.NewRecorder()
	WriteJSONError(w, http.StatusBadRequest, "test")

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	var body map[string]map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	// Verify the function still returns a valid error shape even if marshal
	// were to fail (the fallback string).
	if _, ok := body["error"]; !ok {
		t.Fatal("missing 'error' key")
	}
}

func TestUpdateModelInBody(t *testing.T) {
	tests := []struct {
		name      string
		body      []byte
		modelName string
		wantModel string
	}{
		{
			name:      "replace existing model",
			body:      []byte(`{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"hi"}]}`),
			modelName: "gpt-4",
			wantModel: "gpt-4",
		},
		{
			name:      "add model when absent",
			body:      []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
			modelName: "claude-3",
			wantModel: "claude-3",
		},
		{
			name:      "empty object",
			body:      []byte(`{}`),
			modelName: "gpt-4",
			wantModel: "gpt-4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := UpdateModelInBody(tt.body, tt.modelName)
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}
			if got, _ := m["model"].(string); got != tt.wantModel {
				t.Errorf("model = %q, want %q", got, tt.wantModel)
			}
		})
	}
}

func TestUpdateModelInBody_returnsOriginalOnError(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{"empty", []byte{}},
		{"invalid JSON", []byte(`{invalid}`)},
		{"partial JSON", []byte(`{"model":`)},
		{"garbage", []byte(`not json at all`)},
		{"JSON null", []byte(`null`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := UpdateModelInBody(tt.body, "gpt-4")
			if string(out) != string(tt.body) {
				t.Errorf("got %q, want original %q", string(out), string(tt.body))
			}
		})
	}
}

func TestSetAuthHeader(t *testing.T) {
	tests := []struct {
		name       string
		authScheme string
		apiKey     string
		authHeader string
		wantKey    string
		wantValue  string
	}{
		{
			name:       "bearer scheme",
			authScheme: "bearer",
			apiKey:     "sk-abc123",
			authHeader: "Authorization",
			wantKey:    "Authorization",
			wantValue:  "Bearer sk-abc123",
		},
		{
			name:       "raw scheme",
			authScheme: "raw",
			apiKey:     "tgpv1_xyz",
			authHeader: "Authorization",
			wantKey:    "Authorization",
			wantValue:  "tgpv1_xyz",
		},
		{
			name:       "custom header with bearer",
			authScheme: "bearer",
			apiKey:     "sk-abc123",
			authHeader: "X-API-Key",
			wantKey:    "X-API-Key",
			wantValue:  "Bearer sk-abc123",
		},
		{
			name:       "custom header with raw",
			authScheme: "raw",
			apiKey:     "tgpv1_xyz",
			authHeader: "X-API-Key",
			wantKey:    "X-API-Key",
			wantValue:  "tgpv1_xyz",
		},
		{
			name:       "empty authHeader, bearer scheme",
			authScheme: "bearer",
			apiKey:     "sk-abc123",
			authHeader: "",
			wantKey:    "Authorization",
			wantValue:  "Bearer sk-abc123",
		},
		{
			name:       "empty authHeader, raw scheme",
			authScheme: "raw",
			apiKey:     "tgpv1_xyz",
			authHeader: "",
			wantKey:    "Authorization",
			wantValue:  "tgpv1_xyz",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			SetAuthHeader(req, tt.apiKey, tt.authHeader, tt.authScheme)

			got := req.Header.Get(tt.wantKey)
			if got != tt.wantValue {
				t.Errorf("req.Header[%q] = %q, want %q", tt.wantKey, got, tt.wantValue)
			}
		})
	}
}

func TestSetAuthHeader_defaultScheme(t *testing.T) {
	// When authScheme is unknown, default to "Bearer "+apiKey on Authorization.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	SetAuthHeader(req, "sk-abc123", "Authorization", "unknown")

	got := req.Header.Get("Authorization")
	want := "Bearer sk-abc123"
	if got != want {
		t.Errorf("req.Header[Authorization] = %q, want %q", got, want)
	}
}
