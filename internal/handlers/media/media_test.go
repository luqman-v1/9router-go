package media

import (
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
