package providers

import (
	"net/http"
	"strings"
	"testing"
)

// #338: a combo member that cannot hold the request answers 400 with a message
// naming the model's limit. That is a fact about the model, not the account, so
// the combo must move to a larger member instead of retrying the same too-small
// model on every remaining connection.
func TestIsModelFitError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       bool
	}{
		{
			name:       "openai context_length_exceeded",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"type":"invalid_request_error","message":"This model's maximum context length is 8192 tokens.","code":"context_length_exceeded"}}`,
			want:       true,
		},
		{
			name:       "anthropic prompt is too long",
			statusCode: http.StatusBadRequest,
			body:       `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`,
			want:       true,
		},
		{
			name:       "gemini input too long",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"message":"The input token count (1050000) exceeds the maximum number of tokens allowed (1048576)."}}`,
			want:       true,
		},
		{
			name:       "case and prose do not matter",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"message":"Please reduce the length of the messages."}}`,
			want:       true,
		},
		{
			name:       "422 counts as a fit failure too",
			statusCode: http.StatusUnprocessableEntity,
			body:       `{"error":{"message":"This model's maximum context length is 4096 tokens"}}`,
			want:       true,
		},
		{
			name:       "zed 403 context window exceeded",
			statusCode: http.StatusForbidden,
			body:       `{"error":{"message":"Context window exceeded: 103326 tokens exceeds the limit of 100000 tokens"}}`,
			want:       true,
		},
		{
			name:       "a 403 that is not about fit stays an account refusal",
			statusCode: http.StatusForbidden,
			body:       `{"error":{"message":"you do not have access to this model"}}`,
			want:       false,
		},
		{
			name:       "a malformed body is not a fit failure",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"message":"invalid character 'x' looking for beginning of value"}}`,
			want:       false,
		},
		{
			name:       "a 500 mentioning a context window is not a fit failure",
			statusCode: http.StatusInternalServerError,
			body:       `{"error":{"message":"context window service unavailable"}}`,
			want:       false,
		},
		{
			name:       "a 400 with no body is not a fit failure",
			statusCode: http.StatusBadRequest,
			body:       "",
			want:       false,
		},
		{
			name:       "an auth failure is never a fit failure",
			statusCode: http.StatusUnauthorized,
			body:       `{"error":{"message":"invalid api key"}}`,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsModelFitError(tt.statusCode, []byte(tt.body)); got != tt.want {
				t.Errorf("IsModelFitError(%d, %q) = %v, want %v", tt.statusCode, tt.body, got, tt.want)
			}
		})
	}
}

// The patterns are the public list a caller can extend; pinning that they are
// matched case-insensitively keeps a provider's capitalisation from silently
// turning a fit failure back into a retry.
func TestModelFitErrorPatternsMatchRegardlessOfCase(t *testing.T) {
	for _, pattern := range ModelFitErrorPatterns {
		t.Run(pattern, func(t *testing.T) {
			upper := []byte(`{"error":{"message":"` + strings.ToUpper(pattern) + `"}}`)
			if !IsModelFitError(http.StatusBadRequest, upper) {
				t.Errorf("pattern %q is not matched when upper-cased", pattern)
			}
		})
	}
}
