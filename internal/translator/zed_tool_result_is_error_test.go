package translator

import (
	"testing"
)

func TestToolResultIsError(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected bool
	}{
		{
			name: "explicit is_error true",
			input: map[string]any{
				"type":        "tool_result",
				"tool_use_id": "call_1",
				"content":     "some output",
				"is_error":    true,
			},
			expected: true,
		},
		{
			name: "explicit is_error false",
			input: map[string]any{
				"type":        "tool_result",
				"tool_use_id": "call_1",
				"content":     "some normal output",
				"is_error":    false,
			},
			expected: false,
		},
		{
			name: "camelCase isError true",
			input: map[string]any{
				"type":        "tool_result",
				"tool_use_id": "call_1",
				"content":     "some output",
				"isError":     true,
			},
			expected: true,
		},
		{
			name: "status error string",
			input: map[string]any{
				"role":         "tool",
				"tool_call_id": "call_1",
				"status":       "error",
				"content":      "Command failed",
			},
			expected: true,
		},
		{
			name: "status success string",
			input: map[string]any{
				"role":         "tool",
				"tool_call_id": "call_1",
				"status":       "success",
				"content":      "Command succeeded",
			},
			expected: false,
		},
		{
			name: "JSON error payload in content",
			input: map[string]any{
				"role":         "tool",
				"tool_call_id": "call_1",
				"content":      `{"error":"File not found","code":404}`,
			},
			expected: true,
		},
		{
			name: "JSON is_error true in content",
			input: map[string]any{
				"role":         "tool",
				"tool_call_id": "call_1",
				"content":      `{"is_error":true,"result":"failed"}`,
			},
			expected: true,
		},
		{
			name: "JSON status error in content",
			input: map[string]any{
				"role":         "tool",
				"tool_call_id": "call_1",
				"content":      `{"status":"error","message":"bad syntax"}`,
			},
			expected: true,
		},
		{
			name: "text starting with Error:",
			input: map[string]any{
				"role":         "tool",
				"tool_call_id": "call_1",
				"content":      "Error: file or directory not found",
			},
			expected: true,
		},
		{
			name: "text starting with Exception:",
			input: map[string]any{
				"role":         "tool",
				"tool_call_id": "call_1",
				"content":      "Exception: NullPointerException in line 42",
			},
			expected: true,
		},
		{
			name: "text starting with Traceback",
			input: map[string]any{
				"role":         "tool",
				"tool_call_id": "call_1",
				"content":      "Traceback (most recent call last):\n  File 'test.py', line 1",
			},
			expected: true,
		},
		{
			name: "normal tool output string",
			input: map[string]any{
				"role":         "tool",
				"tool_call_id": "call_1",
				"content":      "file1.txt\nfile2.txt",
			},
			expected: false,
		},
		{
			name: "nested content block with is_error",
			input: map[string]any{
				"type": "tool_result",
				"content": []any{
					map[string]any{
						"type":     "text",
						"is_error": true,
						"text":     "something broke",
					},
				},
			},
			expected: true,
		},
		{
			name:     "nil map",
			input:    nil,
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ToolResultIsError(tc.input)
			if got != tc.expected {
				t.Errorf("ToolResultIsError(%+v) = %v, want %v", tc.input, got, tc.expected)
			}
		})
	}
}
