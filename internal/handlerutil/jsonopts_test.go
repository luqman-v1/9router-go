package handlerutil

import (
	json "encoding/json/v2"
	"testing"
)

// v2 rejects an object that repeats a member name, rejects invalid UTF-8 in
// strings, and matches member names case-sensitively. v1 accepted all three,
// and the payloads routed through this option come from providers and clients
// rather than from this gateway, so the lenient reading has to survive the
// migration.
func TestLenientOptions_AcceptWhatV1Accepted(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "repeated member name", body: `{"role":"user","role":"user"}`},
		{name: "invalid utf-8 in a string", body: "{\"role\":\"caf\xc3\xa9 \xff\"}"},
		{name: "unpaired surrogate escape", body: `{"role":"ok \ud83d"}`},
		{name: "member key spelled with different casing", body: `{"ROLE":"user"}`},
		{name: "repeated name on a field the struct declares", body: `{"role":"a","role":"b"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out struct {
				Role string `json:"role"`
			}
			err := json.Unmarshal([]byte(tt.body), &out, UpstreamBody)
			if err != nil {
				t.Fatalf("Unmarshal(%q) = %v, want nil: v1 accepted this", tt.body, err)
			}
			if out.Role == "" {
				t.Fatalf("Unmarshal(%q) left Role empty, want it matched leniently", tt.body)
			}
		})
	}

	// The option set must not leak into the strict reading used everywhere else.
	var out struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal([]byte(`{"ROLE":"user"}`), &out); err != nil {
		t.Fatalf("strict Unmarshal = %v, want nil (unknown members are ignored by default)", err)
	}
	if out.Role != "" {
		t.Fatalf("strict Unmarshal matched a differently-cased name: Role = %q", out.Role)
	}

	// Without the option set a repeated name on a declared field is an error,
	// so the cases above prove the option set is what made them parse.
	if err := json.Unmarshal([]byte(`{"role":"a","role":"b"}`), &out); err == nil {
		t.Fatal("strict Unmarshal accepted a repeated member name")
	}
}

// A repeated name must still resolve to a value: the lenient set decides that
// such a body parses, not that the result is empty.
func TestLenientOptions_DuplicateNameStillDecodes(t *testing.T) {
	var out struct {
		First  string `json:"first"`
		Second string `json:"second"`
	}
	if err := json.Unmarshal([]byte(`{"first":"a","second":"b"}`), &out, UpstreamBody); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.First != "a" || out.Second != "b" {
		t.Fatalf("got %+v, want first=a second=b", out)
	}
}
