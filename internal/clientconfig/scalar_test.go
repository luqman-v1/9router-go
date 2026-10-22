package clientconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
	yaml "go.yaml.in/yaml/v3"
)

// The three quoters are the only place a value's bytes are produced, so their
// round-trip property is checked once here rather than in each format's suite.

var scalarSamples = []string{
	"",
	"https://api.example.com/v1",
	"plain",
	`quote"inside`,
	`back\slash`,
	`C:\Program Files\x`,
	"tab\there",
	"new\nline",
	"crlf\r\nline",
	"ctrl\x01byte",
	"del\x7fbyte",
	"unicode: tiếng Việt, ünï, 日本語",
	"#hash: colon, comma, bracket]",
	"'single' and \"double\"",
	"\u2028line separator",
}

func TestJSONCQuoteRoundTrip(t *testing.T) {
	for _, s := range scalarSamples {
		t.Run(s, func(t *testing.T) {
			q := jsoncQuote(s)
			if !strings.HasPrefix(q, `"`) || !strings.HasSuffix(q, `"`) {
				t.Fatalf("jsoncQuote(%q) = %q, want a quoted literal", s, q)
			}
			var back string
			if err := json.Unmarshal([]byte(q), &back); err != nil {
				t.Fatalf("jsoncQuote(%q) = %q does not parse: %v", s, q, err)
			}
			if back != s {
				t.Fatalf("jsoncQuote round-trip = %q, want %q", back, s)
			}
			if strings.Contains(q, `\/`) {
				t.Errorf("jsoncQuote(%q) escaped a slash: %q", s, q)
			}
		})
	}
}

func TestTOMLQuoteRoundTrip(t *testing.T) {
	for _, s := range scalarSamples {
		t.Run(s, func(t *testing.T) {
			q := tomlQuote(s)
			if !strings.HasPrefix(q, `"`) || !strings.HasSuffix(q, `"`) {
				t.Fatalf("tomlQuote(%q) = %q, want a basic string", s, q)
			}
			var doc struct {
				K string `toml:"k"`
			}
			if err := toml.Unmarshal([]byte("k = "+q+"\n"), &doc); err != nil {
				t.Fatalf("tomlQuote(%q) = %q does not parse: %v", s, q, err)
			}
			if doc.K != s {
				t.Fatalf("tomlQuote round-trip = %q, want %q", doc.K, s)
			}
			if !strings.Contains(q, `\"`) && strings.Contains(s, `"`) {
				t.Errorf("tomlQuote(%q) = %q did not escape the quote", s, q)
			}
		})
	}
}

func TestYAMLQuoteRoundTrip(t *testing.T) {
	for _, s := range scalarSamples {
		t.Run(s, func(t *testing.T) {
			q := yamlQuote(s)
			if !strings.HasPrefix(q, `"`) || !strings.HasSuffix(q, `"`) {
				t.Fatalf("yamlQuote(%q) = %q, want a double-quoted scalar", s, q)
			}
			var doc struct {
				K string `yaml:"k"`
			}
			if err := yaml.Unmarshal([]byte("k: "+q+"\n"), &doc); err != nil {
				t.Fatalf("yamlQuote(%q) = %q does not parse: %v", s, q, err)
			}
			if doc.K != s {
				t.Fatalf("yamlQuote round-trip = %q, want %q", doc.K, s)
			}
			if strings.HasSuffix(q, " ") || strings.Contains(q, "\n") {
				t.Errorf("yamlQuote(%q) = %q must be single-line and unpadded", s, q)
			}
		})
	}
}

func TestQuoteIsStable(t *testing.T) {
	// §6 requires byte-identical output for identical input; a quoter that
	// varied would turn every patch into a spurious rewrite.
	//
	// The quoters are pure functions of their argument, so asking whether one
	// call equals another call with the same argument tests nothing — it is
	// true by construction. What the property actually needs is that the
	// quoter produces the same bytes for the same *value* when the value
	// arrives through the patcher, so each sample is written into a real
	// document and then patched again: the second pass must find the value
	// already there and change nothing.
	//
	// Every sample goes through all three formats, because each format has its
	// own quoter and a value that is stable in one is not automatically stable
	// in another.
	for _, s := range scalarSamples {
		t.Run(s, func(t *testing.T) {
			cases := []struct {
				name   string
				format Format
				src    string
				path   []string
			}{
				{"jsonc", FormatJSONC, "{\n  \"v\": \"old\"\n}\n", []string{"v"}},
				{"toml", FormatTOML, "[t]\nv = \"old\"\n", []string{"t", "v"}},
				{"yaml", FormatYAML, "v: old\n", []string{"v"}},
			}
			for _, tc := range cases {
				edit := Edit{KeyPath: tc.path, Value: s}
				first, err := Patch([]byte(tc.src), tc.format, []Edit{edit})
				if err != nil {
					t.Fatalf("%s: first patch: %v", tc.name, err)
				}
				second, err := Patch(first, tc.format, []Edit{edit})
				if !errors.Is(err, ErrAlreadyPatched) {
					t.Fatalf("%s: second patch error = %v, want ErrAlreadyPatched", tc.name, err)
				}
				if !bytes.Equal(second, first) {
					t.Fatalf("%s: second patch changed bytes:\n%q\n%q", tc.name, first, second)
				}
			}
		})
	}
}
