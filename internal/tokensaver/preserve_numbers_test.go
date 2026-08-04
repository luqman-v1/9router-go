package tokensaver

import (
	"encoding/json"
	"strings"
	"testing"
)

// CompressMessages must not corrupt numeric fields (temperature, top_p, an
// arbitrary large int) when it re-marshals — a plain map[string]any round-trip
// coerces numbers to float64 and changes their representation. UseNumber()
// preserves the original literal.
func TestCompressMessages_PreservesNumericFields(t *testing.T) {
	bigContent := strings.Repeat("x", MinCompressSize+10)
	body := []byte(`{
		"model": "gpt-4o",
		"temperature": 0.7,
		"top_p": 1,
		"large_id": 9007199254740993,
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "tool", "tool_call_id": "t1", "content": "` + bigContent + `"}
		]
	}`)

	out, changed := CompressMessages(body)
	if !changed {
		t.Fatal("expected compression to apply")
	}

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("re-marshal invalid: %v", err)
	}
	if nt, ok := m["temperature"].(float64); !ok || nt != 0.7 {
		t.Errorf("temperature corrupted: %v (%T)", m["temperature"], m["temperature"])
	}
	if np, ok := m["top_p"].(float64); !ok || np != 1 {
		t.Errorf("top_p corrupted: %v (%T)", m["top_p"], m["top_p"])
	}
	// 9007199254740993 is not exactly representable as float64; if coerced it
	// rounds to 9007199254740992. Plain json round-trip loses the value.
	if s := strings.Contains(string(out), "9007199254740993"); !s {
		t.Errorf("large int lost precision in re-marshal: %s", string(out))
	}
}