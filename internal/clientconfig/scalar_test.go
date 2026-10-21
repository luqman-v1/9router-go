package clientconfig

import (
	"encoding/json"
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

func TestQuoteIsDeterministic(t *testing.T) {
	// §6 requires byte-identical output for identical input; a quoter that
	// varied would turn every patch into a spurious rewrite.
	for _, s := range scalarSamples {
		if jsoncQuote(s) != jsoncQuote(s) || tomlQuote(s) != tomlQuote(s) || yamlQuote(s) != yamlQuote(s) {
			t.Fatalf("quoter is not deterministic for %q", s)
		}
	}
}
