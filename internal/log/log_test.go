package log

import (
	"context"
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestMaskSecret(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"short", "***"},
		{"sk-proj-1234567890", "sk-p...7890"},
		{"12345678", "***"},
		{"123456789", "1234...6789"},
	}

	for _, tt := range tests {
		got := MaskSecret(tt.input)
		if got != tt.want {
			t.Errorf("MaskSecret(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestJSONLogOutput(t *testing.T) {
	SetJSONFormat(true)
	defer SetJSONFormat(false)

	var buf bytes.Buffer
	log.SetOutput(&buf)

	ctx := context.WithValue(context.Background(), "requestID", "req_test123")
	InfoCtx(ctx, "test", "hello JSON log", "key", "value")

	output := buf.String()
	if !strings.Contains(output, `"tag":"test"`) {
		t.Errorf("expected tag JSON in output, got: %s", output)
	}
	if !strings.Contains(output, `"req_id":"req_test123"`) {
		t.Errorf("expected req_id JSON in output, got: %s", output)
	}
}
