package fastjson

import (
	"strings"
	"testing"
)

type sample struct {
	Name  string   `json:"name"`
	Count int      `json:"count"`
	Tags  []string `json:"tags,omitempty"`
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	in := sample{Name: "9router", Count: 42, Tags: []string{"a", "b"}}
	data, err := Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out sample
	if err := Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.Name != in.Name || out.Count != in.Count || len(out.Tags) != 2 {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", out, in)
	}
}

func TestMarshalNoHTMLEscape(t *testing.T) {
	data, err := Marshal(map[string]string{"expr": "1 < 2 && 3 > 2"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), `\u003c`) {
		t.Fatalf("output HTML-escaped unexpectedly: %s", data)
	}
}

// Upstream prompt caches key on exact request bytes, so two marshals of the
// same value must be byte-identical regardless of map iteration order.
func TestMarshalIsDeterministic(t *testing.T) {
	v := map[string]int{"z": 1, "a": 2, "m": 3, "b": 4}
	first, err := Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"a":2,"b":4,"m":3,"z":1}`
	if string(first) != want {
		t.Fatalf("Marshal = %s, want %s", first, want)
	}
	for range 50 {
		again, err := Marshal(v)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("Marshal not deterministic: %s then %s", first, again)
		}
	}
}

func TestUnmarshalError(t *testing.T) {
	var out sample
	if err := Unmarshal([]byte(`{"count": "not-an-int"}`), &out); err == nil {
		t.Fatal("expected error unmarshaling wrong type")
	}
}

func TestUnmarshalRead(t *testing.T) {
	r := strings.NewReader(`{"name":"read_test","count":10}`)
	var out sample
	if err := UnmarshalRead(r, &out); err != nil {
		t.Fatalf("UnmarshalRead: %v", err)
	}
	if out.Name != "read_test" || out.Count != 10 {
		t.Fatalf("UnmarshalRead mismatch: got %+v", out)
	}
}
