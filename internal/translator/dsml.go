package translator

import (
	"encoding/json"
	"regexp"
	"strings"
)

// deepSeekDSMLMarker recognises the DSML namespace in any of its spellings:
//
//	<|DSML| ...>  <‖‖DSML‖‖ ...>  <｜｜DSML｜｜ ...>  <|‖DSML‖| ...>
//
// The bar may be ASCII or full-width, padded with spaces, one or two bars deep.
var (
	dsMLMarker = regexp.MustCompile(`[｜|‖]{1,2}\s*DSML\s*[｜|‖]{1,2}`)

	dsmlInvokeOpen     = regexp.MustCompile(`<[｜|‖]{1,2}\s*DSML\s*[｜|‖]{1,2}\s*invoke\b`)
	dsmlInvokeClose    = regexp.MustCompile(`</?[｜|‖]{1,2}\s*DSML\s*[｜|‖]{1,2}\s*invoke\s*>`)
	dsmlParameterOpen  = regexp.MustCompile(`<[｜|‖]{1,2}\s*DSML\s*[｜|‖]{1,2}\s*parameter\b`)
	dsmlParameterClose = regexp.MustCompile(`</?[｜|‖]{1,2}\s*DSML\s*[｜|‖]{1,2}\s*parameter\s*>`)

	dsmlAnyOpen  = regexp.MustCompile(`<[｜|‖]{1,2}\s*DSML\s*[｜|‖]{1,2}`)
	dsmlAnyClose = regexp.MustCompile(`</[｜|‖]{1,2}\s*DSML\s*[｜|‖]{1,2}`)
)

// DSMLToolRequest is one parsed DSML tool invocation.
type DSMLToolRequest struct {
	ID        string
	Name      string
	Arguments string
	Raw       string
}

// ExtractDSMLToolCalls parses every DSML tool invocation block out of text and
// returns the prose that surrounded it (with all DSML markup stripped). Only
// invokes naming a tool the client declared are returned; the caller decides
// which names are acceptable.
//
// The text returned is the input minus every DSML element — tags, parameter
// bodies, stray closers — so callers that stream content to a client never
// leak raw XML fragments.
func ExtractDSMLToolCalls(text string, toolNames []string) (prose string, calls []DSMLToolRequest) {
	invokes := dsmlInvokes(text)
	if len(invokes) == 0 {
		return deepSeekStripDSML(text), nil
	}

	declared := make(map[string]bool, len(toolNames))
	filter := len(toolNames) > 0
	for _, n := range toolNames {
		declared[n] = true
	}

	calls = make([]DSMLToolRequest, 0, len(invokes))
	for _, invoke := range invokes {
		call, ok := parseDSMLInvoke(invoke)
		if !ok || (filter && !declared[call.Name]) {
			continue
		}
		calls = append(calls, call)
	}

	// Strip every DSML element — invokes, parameters, and the calls wrapper —
	// so no markup reaches the client, only the prose around it.
	return deepSeekStripDSML(text), calls
}

// dsmlInvokes returns every invoke element verbatim, including its tags.
func dsmlInvokes(text string) []string {
	var invokes []string
	rest := text
	for {
		openAt := dsmlInvokeOpen.FindStringIndex(rest)
		if openAt == nil {
			return invokes
		}
		rest = rest[openAt[0]:]

		openEnd := strings.Index(rest, ">")
		if openEnd < 0 {
			return invokes
		}
		head := rest[:openEnd+1]
		rest = rest[openEnd+1:]

		closeAt := dsmlInvokeClose.FindStringIndex(rest)
		if closeAt == nil {
			return invokes
		}
		invokes = append(invokes, head+rest[:closeAt[1]])
		rest = rest[closeAt[1]:]
	}
}

// parseDSMLInvoke turns one invoke element into a DSMLToolRequest.
func parseDSMLInvoke(invoke string) (DSMLToolRequest, bool) {
	attributes, body, found := strings.Cut(invoke, ">")
	if !found {
		return DSMLToolRequest{}, false
	}

	name := dsmlAttribute(attributes, "name")
	if name == "" {
		return DSMLToolRequest{}, false
	}

	return DSMLToolRequest{
		ID:        "",
		Name:      name,
		Arguments: dsmlArguments(body),
		Raw:       invoke,
	}, true
}

// dsmlAttribute reads one quoted attribute value, matched as a whole attribute.
func dsmlAttribute(attributes, key string) string {
	marker := key + "="
	rest := attributes
	for {
		at := strings.Index(rest, marker)
		if at < 0 {
			return ""
		}
		wholeAttribute := at == 0 || isAttrBreak(rest[at-1])
		rest = rest[at+len(marker):]
		if !wholeAttribute {
			continue
		}
		if rest == "" {
			return ""
		}
		quote := rest[0]
		if quote != '"' && quote != '\'' {
			return ""
		}
		value, _, ok := strings.Cut(rest[1:], string(quote))
		if !ok {
			return ""
		}
		return value
	}
}

func isAttrBreak(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// dsmlArguments builds the JSON arguments object from parameter elements,
// sorted by name for byte-identical output across identical turns.
func dsmlArguments(body string) string {
	params := dsmlParameters(body)
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sortStrings(names)

	var b strings.Builder
	b.WriteString("{")
	for i, name := range names {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(jsonQuote(name))
		b.WriteString(":")
		b.WriteString(dsmlValue(params[name]))
	}
	b.WriteString("}")
	return b.String()
}

// dsmlParameters returns every parameter element's body, keyed by name.
func dsmlParameters(body string) map[string]string {
	params := make(map[string]string)
	rest := body
	for {
		openAt := dsmlParameterOpen.FindStringIndex(rest)
		if openAt == nil {
			return params
		}
		rest = rest[openAt[1]:]

		openEnd := strings.Index(rest, ">")
		if openEnd < 0 {
			return params
		}
		attributes := rest[:openEnd]
		rest = rest[openEnd+1:]

		closeAt := dsmlParameterClose.FindStringIndex(rest)
		if closeAt == nil {
			return params
		}
		if name := dsmlAttribute(attributes, "name"); name != "" {
			params[name] = rest[:closeAt[0]]
		}
		rest = rest[closeAt[1]:]
	}
}

// dsmlValue renders one parameter body as a JSON string value.
func dsmlValue(body string) string {
	return jsonQuote(strings.TrimSpace(body))
}

func jsonQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

func sortStrings(names []string) {
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
}

// dsmlToolCallsDSML emits DSML tool calls found in a completed response's
// content as OpenAI tool calls and strips DSML markup from the remaining
// content text. Returns the cleaned content and the parsed calls.
func extractDSMLFromContent(content string, toolNames []string) (string, []DSMLToolRequest) {
	prose, calls := ExtractDSMLToolCalls(content, toolNames)
	return prose, calls
}

// deepSeekStripDSML removes every DSML element from an answer. Used wherever
// the markup could otherwise reach the user: a block that could not be turned
// into a call, and the text around a block that could.
func deepSeekStripDSML(text string) string {
	if !dsMLMarker.MatchString(text) {
		return text
	}
	var scrubber dsmlScrubber
	clean, _ := scrubber.scrub(text)
	return strings.TrimSpace(clean)
}

// dsmlScrubber strips DSML markup out of a character stream as it flows past,
// keeping only the prose around it.
type dsmlScrubber struct {
	depth int
}

// scrub consumes as much of tail as is decidable and returns the clean text
// along with the number of bytes consumed.
func (s *dsmlScrubber) scrub(tail string) (string, int) {
	var out strings.Builder
	off := 0
	for off < len(tail) {
		if s.depth > 0 {
			tag, ok := dsmlNextDSMLTag(tail[off:])
			if !ok {
				return out.String(), off
			}
			off += tag.end
			s.depth += tag.openDelta
			if s.depth < 0 {
				s.depth = 0
			}
			continue
		}

		openAt := dsmlAnyOpen.FindStringIndex(tail[off:])
		closeAt := dsmlAnyClose.FindStringIndex(tail[off:])
		var at []int
		switch {
		case openAt != nil && closeAt != nil:
			if openAt[0] < closeAt[0] {
				at = openAt
			} else {
				at = closeAt
			}
		case openAt != nil:
			at = openAt
		case closeAt != nil:
			at = closeAt
		default:
			if cut := dsmlTagPrefixCut(tail[off:]); cut >= 0 {
				return out.String(), off + cut
			}
			out.WriteString(tail[off:])
			return out.String(), len(tail)
		}

		out.WriteString(tail[off : off+at[0]])
		off += at[0]
		tag, ok := dsmlNextDSMLTag(tail[off:])
		if !ok {
			return out.String(), off
		}
		if tag.openDelta > 0 {
			s.depth = 1
		}
		off += tag.end
	}
	return out.String(), off
}

func dsmlTagPrefixCut(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '<' && dsmlTagPrefix(s[i:]) {
			return i
		}
	}
	return -1
}

func dsmlTagPrefix(s string) bool {
	if !strings.HasPrefix(s, "<") {
		return false
	}
	rest := s[1:]
	bars := 0
	for bars < 2 && len(rest) > 0 && (rest[0] == '|' || strings.HasPrefix(rest, "｜")) {
		if rest[0] == '|' {
			rest = rest[1:]
		} else {
			rest = rest[len("｜"):]
		}
		bars++
	}
	if bars == 0 && len(rest) > 0 {
		return false
	}
	rest = trimDSMLSpace(rest, 4)
	const dsml = "DSML"
	n := len(rest)
	if n > len(dsml) {
		n = len(dsml)
	}
	if n > 0 && rest[:n] != dsml[:n] {
		return false
	}
	rest = rest[n:]
	if n < len(dsml) {
		return true
	}
	rest = trimDSMLSpace(rest, 4)
	bars = 0
	for bars < 2 && len(rest) > 0 && (rest[0] == '|' || strings.HasPrefix(rest, "｜")) {
		if rest[0] == '|' {
			rest = rest[1:]
		} else {
			rest = rest[len("｜"):]
		}
		bars++
	}
	return len(rest) == 0
}

func trimDSMLSpace(s string, max int) string {
	n := 0
	for n < max && len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
		n++
	}
	return s
}

// dsmlDSMLTag is one complete DSML tag found in text.
type dsmlDSMLTag struct {
	openDelta int // +1 opening, -1 closing
	end       int // offset just past the tag's `>`
}

// dsmlNextDSMLTag finds the next complete DSML tag — open or close — in text.
func dsmlNextDSMLTag(text string) (dsmlDSMLTag, bool) {
	open := dsmlAnyOpen.FindStringIndex(text)
	close := dsmlAnyClose.FindStringIndex(text)
	var start int
	switch {
	case open != nil && close != nil:
		if open[0] < close[0] {
			start = open[0]
		} else {
			start = close[0]
		}
	case open != nil:
		start = open[0]
	case close != nil:
		start = close[0]
	default:
		return dsmlDSMLTag{}, false
	}
	tagEnd := strings.Index(text[start:], ">")
	if tagEnd < 0 {
		return dsmlDSMLTag{}, false
	}
	openDelta := 1
	if len(text) > start+1 && text[start+1] == '/' {
		openDelta = -1
	}
	return dsmlDSMLTag{openDelta: openDelta, end: start + tagEnd + 1}, true
}
