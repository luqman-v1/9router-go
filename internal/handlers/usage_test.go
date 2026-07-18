package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/translator"
)

func TestExtractContent(t *testing.T) {
	tests := []struct {
		name    string
		content any
		want    string
	}{
		{"string content", "hello world", "hello world"},
		{
			"array of text blocks",
			[]any{
				map[string]any{"text": "first"},
				map[string]any{"text": "second"},
			},
			"first second",
		},
		{"empty array", []any{}, ""},
		{"non-string non-array (number)", 42, "42"},
		{"nil content", nil, "<nil>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractContent(tt.content)
			if got != tt.want {
				t.Errorf("extractContent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMaskAPIKeyPure(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{"short key (< 8 chars)", "abc", "***"},
		{"exact 8 chars", "12345678", "***"},
		{"long key", "sk-1234567890abcdef12345678", "sk-1***5678"},
		{"empty key", "", "***"},
		{"7 chars", "1234567", "***"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maskAPIKey(tt.key)
			if got != tt.want {
				t.Errorf("maskAPIKey(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestParseDailyDataPure(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want func(map[string]any) bool
	}{
		{
			"empty string", "",
			func(m map[string]any) bool { return len(m) == 0 },
		},
		{
			"valid JSON", `{"requests":5,"cost":1.5}`,
			func(m map[string]any) bool {
				return m["requests"].(float64) == 5 && m["cost"].(float64) == 1.5
			},
		},
		{
			"malformed JSON", "not json",
			func(m map[string]any) bool { return len(m) == 0 },
		},
		{
			"JSON null", "null",
			func(m map[string]any) bool { return len(m) == 0 },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDailyData(tt.raw)
			if got == nil {
				t.Fatal("parseDailyData() returned nil")
			}
			if !tt.want(got) {
				t.Errorf("parseDailyData(%q) = %v, want condition satisfied", tt.raw, got)
			}
		})
	}
}

func TestGetJSONIntComprehensive(t *testing.T) {
	m := map[string]any{
		"count":   float64(42),
		"invalid": "not a number",
	}
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want int
	}{
		{"float64 stored", m, "count", 42},
		{"missing key", m, "missing", 0},
		{"non-numeric value", m, "invalid", 0},
		{"nil map", nil, "key", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getJSONInt(tt.m, tt.key)
			if got != tt.want {
				t.Errorf("getJSONInt() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGetJSONFloatComprehensive(t *testing.T) {
	m := map[string]any{
		"price":   float64(3.14),
		"invalid": "not a float",
	}
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want float64
	}{
		{"float64 stored", m, "price", 3.14},
		{"missing key", m, "missing", 0},
		{"non-numeric value", m, "invalid", 0},
		{"nil map", nil, "key", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getJSONFloat(tt.m, tt.key)
			if got != tt.want {
				t.Errorf("getJSONFloat() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetJSONMapComprehensive(t *testing.T) {
	nested := map[string]any{"foo": "bar"}
	m := map[string]any{
		"existing":  nested,
		"notamap":   "stringval",
	}
	tests := []struct {
		name string
		m    map[string]any
		key  string
	}{
		{"existing map", m, "existing"},
		{"missing key creates new map", m, "newKey"},
		{"non-map value returns new map", m, "notamap"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getJSONMap(tt.m, tt.key)
			if got == nil {
				t.Fatal("getJSONMap() returned nil")
			}
			if tt.key == "existing" {
				if got["foo"] != "bar" {
					t.Errorf("getJSONMap() = %v, want nested map with foo=bar", got)
				}
			}
		})
	}
}

func TestExtractRequestMessages(t *testing.T) {
	t.Run("valid messages", func(t *testing.T) {
		body := buildRequestBody([]translator.OpenAIMessage{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "world"},
		})
		msgs := extractRequestMessages(body)
		if len(msgs) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(msgs))
		}
		if msgs[0]["role"] != "user" || msgs[0]["content"] != "hello" {
			t.Errorf("first message mismatch: %v", msgs[0])
		}
		if msgs[1]["role"] != "assistant" || msgs[1]["content"] != "world" {
			t.Errorf("second message mismatch: %v", msgs[1])
		}
	})

	t.Run("truncated content", func(t *testing.T) {
		longContent := strings.Repeat("a", constants.MaxMessageContentLen+100)
		body := buildRequestBody([]translator.OpenAIMessage{
			{Role: "user", Content: longContent},
		})
		msgs := extractRequestMessages(body)
		if len(msgs) != 1 {
			t.Fatalf("expected 1 message, got %d", len(msgs))
		}
		expected := longContent[:constants.MaxMessageContentLen] + "..."
		if msgs[0]["content"] != expected {
			t.Errorf("content truncated: got %d chars, want %d",
				len(msgs[0]["content"]), len(expected))
		}
	})

	t.Run("exceeds max logged messages", func(t *testing.T) {
		msgs := make([]translator.OpenAIMessage, constants.MaxLoggedMessages+5)
		for i := range msgs {
			msgs[i] = translator.OpenAIMessage{
				Role:    "user",
				Content: "msg",
			}
		}
		body := buildRequestBody(msgs)
		result := extractRequestMessages(body)
		if len(result) != constants.MaxLoggedMessages {
			t.Fatalf("expected %d messages, got %d", constants.MaxLoggedMessages, len(result))
		}
		// Keep last MaxLoggedMessages
		if result[0]["content"] != "msg" {
			t.Errorf("expected last messages, got %v", result[0])
		}
	})

	t.Run("empty body", func(t *testing.T) {
		msgs := extractRequestMessages([]byte{})
		if msgs != nil {
			t.Errorf("expected nil for empty body, got %v", msgs)
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		msgs := extractRequestMessages([]byte("not json"))
		if msgs != nil {
			t.Errorf("expected nil for malformed JSON, got %v", msgs)
		}
	})

	t.Run("no messages in valid JSON", func(t *testing.T) {
		body := buildRequestBody([]translator.OpenAIMessage{})
		msgs := extractRequestMessages(body)
		if msgs != nil {
			t.Errorf("expected nil for empty messages, got %v", msgs)
		}
	})
}

func buildRequestBody(msgs []translator.OpenAIMessage) []byte {
	req := translator.OpenAIRequest{
		Model:    "test-model",
		Messages: msgs,
	}
	b, _ := json.Marshal(req)
	return b
}
