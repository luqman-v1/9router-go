package translator

import (
	"encoding/json"
	"strings"
	"testing"
)

// Reproduces the Gemini 400 "Unknown name const": a nullable property
// declared as anyOf with a const branch. Flattening re-injects const.
func TestCleanParametersSchema_anyOfConst(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"value": {"anyOf": [{"type": "string", "const": "A"}, {"type": "null"}]}
		}
	}`)
	out := CleanParametersSchema(schema)
	if strings.Contains(string(out), "const") {
		t.Fatalf("const survived anyOf flatten:\n%s", string(out))
	}
}

func TestCleanParametersSchema_oneOfConst(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"value": {"oneOf": [{"type": "string", "const": "X"}, {"type": "null"}]}
		}
	}`)
	out := CleanParametersSchema(schema)
	if strings.Contains(string(out), "const") {
		t.Fatalf("const survived oneOf flatten:\n%s", string(out))
	}
}
