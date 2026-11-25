package media

import (
	json "encoding/json/v2"
	"reflect"
	"testing"
)

func TestBuildEmbeddingsURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "replace chat completions suffix",
			baseURL: "https://api.openai.com/v1/chat/completions",
			want:    "https://api.openai.com/v1/embeddings",
		},
		{
			name:    "append embeddings when no chat completions",
			baseURL: "https://api.openai.com/v1",
			want:    "https://api.openai.com/v1/embeddings",
		},
		{
			name:    "base URL with trailing slash",
			baseURL: "https://api.openai.com/v1/",
			want:    "https://api.openai.com/v1/embeddings",
		},
		{
			name:    "base URL without path",
			baseURL: "https://api.openai.com",
			want:    "https://api.openai.com/embeddings",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildEmbeddingsURL(tc.baseURL)
			if got != tc.want {
				t.Errorf("buildEmbeddingsURL(%q) = %q, want %q", tc.baseURL, got, tc.want)
			}
		})
	}
}

func TestFormatXAIVideoPayload(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantJSON string
	}{
		{
			name:     "string image wrapped in url object",
			input:    `{"model":"xai/grok-imagine-video","prompt":"animate it","image":"https://example.com/frame.jpg"}`,
			wantJSON: `{"image":{"url":"https://example.com/frame.jpg"},"model":"xai/grok-imagine-video","prompt":"animate it"}`,
		},
		{
			name:     "object image with url preserved",
			input:    `{"model":"xai/grok-imagine-video","prompt":"animate it","image":{"url":"data:image/jpeg;base64,AAAA"}}`,
			wantJSON: `{"image":{"url":"data:image/jpeg;base64,AAAA"},"model":"xai/grok-imagine-video","prompt":"animate it"}`,
		},
		{
			name:     "object image with file_id preserved",
			input:    `{"model":"xai/grok-imagine-video","prompt":"animate it","image":{"file_id":"file-123"}}`,
			wantJSON: `{"image":{"file_id":"file-123"},"model":"xai/grok-imagine-video","prompt":"animate it"}`,
		},
		{
			name:     "reference_images array forwarded",
			input:    `{"model":"xai/grok-imagine-video","prompt":"animate it","image":"https://example.com/frame.jpg","reference_images":[{"url":"https://example.com/style.jpg"}]}`,
			wantJSON: `{"image":{"url":"https://example.com/frame.jpg"},"model":"xai/grok-imagine-video","prompt":"animate it","reference_images":[{"url":"https://example.com/style.jpg"}]}`,
		},
		{
			name:     "invalid image type stripped",
			input:    `{"model":"xai/grok-imagine-video","prompt":"animate it","image":42}`,
			wantJSON: `{"model":"xai/grok-imagine-video","prompt":"animate it"}`,
		},
		{
			name:     "non-array reference_images stripped",
			input:    `{"model":"xai/grok-imagine-video","prompt":"animate it","reference_images":"not-an-array"}`,
			wantJSON: `{"model":"xai/grok-imagine-video","prompt":"animate it"}`,
		},
		{
			name:     "no image untouched",
			input:    `{"model":"xai/grok-imagine-video","prompt":"a cat running"}`,
			wantJSON: `{"model":"xai/grok-imagine-video","prompt":"a cat running"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatXAIVideoPayload([]byte(tc.input))
			var gotMap, wantMap map[string]any
			if err := json.Unmarshal(got, &gotMap); err != nil {
				t.Fatalf("failed to unmarshal got: %v", err)
			}
			if err := json.Unmarshal([]byte(tc.wantJSON), &wantMap); err != nil {
				t.Fatalf("failed to unmarshal want: %v", err)
			}
			if !reflect.DeepEqual(gotMap, wantMap) {
				t.Errorf("formatXAIVideoPayload() mismatch\ngot:  %+v\nwant: %+v", gotMap, wantMap)
			}
		})
	}
}
