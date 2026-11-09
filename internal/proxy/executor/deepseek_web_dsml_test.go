package executor

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// liveDSMLAnswer is a DSML tool call copied out of a real answer, byte for byte,
// newlines and all. It is not reconstructed from a reading of the format.
//
// That distinction is the whole reason this file exists twice over. The first
// version of it wrote the closing tags without a slash — `<｜｜DSML｜｜ invoke>` —
// because that is what the notation looks like when it is described in prose. The
// live form closes with a slash, `</｜｜DSML｜｜ invoke>`, and a parser keyed on the
// invented spelling matched nothing at all: every DSML call stayed unparsed and
// reached the client as literal markup. A test that had used the real bytes would
// have failed immediately.
const liveDSMLAnswer = `<｜｜DSML｜｜ calls>
<｜｜DSML｜｜ invoke name="read">
<｜｜DSML｜｜ parameter name="path" string="true">https://wttr.in/Jakarta?format=j1</｜｜DSML｜｜ parameter>
<｜｜DSML｜｜ parameter name="i" string="true">Fetching Jakarta weather</｜｜DSML｜｜ parameter>
</｜｜DSML｜｜ invoke>
<｜｜DSML｜｜ invoke name="read">
<｜｜DSML｜｜ parameter name="path" string="true">https://wttr.in/Tokyo?format=j1</｜｜DSML｜｜ parameter>
<｜｜DSML｜｜ parameter name="i" string="true">Fetching Tokyo weather</｜｜DSML｜｜ parameter>
</｜｜DSML｜｜ invoke>
</｜｜DSML｜｜ calls>`

func readTool() deepSeekToolSpec {
	var spec deepSeekToolSpec
	spec.Type = "function"
	spec.Function.Name = "read"
	spec.Function.Description = "read a file or url"
	return spec
}

// The whole point: a DSML answer reached the client as literal markup, because the
// parser never matched the real closing tags. The client then had no call to run
// and read the whole turn as a refusal.
func TestDeepSeekExtractsDSMLToolCall(t *testing.T) {
	call, ok := extractOne(liveDSMLAnswer, []deepSeekToolSpec{readTool()})
	if !ok {
		t.Fatal("a DSML call was not recognised")
	}
	if call.Name != "read" {
		t.Errorf("name = %q, want read", call.Name)
	}

	var args struct {
		Path string `json:"path"`
		Info string `json:"i"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		t.Fatalf("arguments are not a JSON object: %v\n%s", err, call.Arguments)
	}
	if args.Path != "https://wttr.in/Jakarta?format=j1" {
		t.Errorf("path = %q, want the first invoke's url", args.Path)
	}
	if args.Info != "Fetching Jakarta weather" {
		t.Errorf("i = %q, want the first invoke's info", args.Info)
	}
}

// The close tags are recognised in both spellings. A harness that omits the slash
// has to keep working, and a parser that only accepts one of them fails silently.
func TestDeepSeekDSMLAcceptsBothCloseSpellings(t *testing.T) {
	tests := []struct {
		name  string
		close string
	}{
		{name: "with slash", close: "</｜｜DSML｜｜ invoke>"},
		{name: "without slash", close: "<｜｜DSML｜｜ invoke>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			answer := "<｜｜DSML｜｜ invoke name=\"read\">" +
				"<｜｜DSML｜｜ parameter name=\"path\" string=\"true\">/tmp/x</｜｜DSML｜｜ parameter>" +
				tt.close

			call, ok := extractOne(answer, []deepSeekToolSpec{readTool()})
			if !ok {
				t.Fatalf("close tag %q was not recognised", tt.close)
			}
			if !strings.Contains(call.Arguments, "/tmp/x") {
				t.Errorf("arguments = %s, want the path", call.Arguments)
			}
		})
	}
}

// An ASCII pipe is the same marker: the model emits either, and a build that
// normalises the text upstream may hand over the ASCII spelling.
func TestDeepSeekDSMLAcceptsASCIIPipe(t *testing.T) {
	answer := "<||DSML|| invoke name=\"read\">" +
		"<||DSML|| parameter name=\"path\" string=\"true\">/tmp/x</||DSML|| parameter>" +
		"</||DSML|| invoke>"

	call, ok := extractOne(answer, []deepSeekToolSpec{readTool()})
	if !ok {
		t.Fatal("an ASCII-pipe block was not recognised")
	}
	if !strings.Contains(call.Arguments, "/tmp/x") {
		t.Errorf("arguments = %s, want the path", call.Arguments)
	}
}

// A parameter value is element text and must survive verbatim: trimming or
// re-quoting it changes what the tool receives.
func TestDeepSeekDSMLKeepsParameterTextVerbatim(t *testing.T) {
	answer := "<｜｜DSML｜｜ invoke name=\"read\">" +
		"<｜｜DSML｜｜ parameter name=\"path\" string=\"true\">a \"quoted\" value</｜｜DSML｜｜ parameter>" +
		"</｜｜DSML｜｜ invoke>"

	call, ok := extractOne(answer, []deepSeekToolSpec{readTool()})
	if !ok {
		t.Fatal("a single un-wrapped invoke was not recognised")
	}

	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		t.Fatalf("arguments do not parse: %v", err)
	}
	if args.Path != `a "quoted" value` {
		t.Errorf("path = %q, want the quotes preserved", args.Path)
	}
}

// An undeclared tool must not be forwarded, in this dialect either.
func TestDeepSeekDSMLUndeclaredToolIsRejected(t *testing.T) {
	answer := "<｜｜DSML｜｜ invoke name=\"delete_everything\">" +
		"<｜｜DSML｜｜ parameter name=\"path\" string=\"true\">/</｜｜DSML｜｜ parameter>" +
		"</｜｜DSML｜｜ invoke>"

	if _, ok := extractOne(answer, []deepSeekToolSpec{readTool()}); ok {
		t.Error("a tool the client never declared must not be returned as a call")
	}
}

// The same conservative rule as the JSON path: markup that names no tool is not
// an answer, and showing it to the user reads as corruption.
func TestDeepSeekStripsUnparsedDSML(t *testing.T) {
	got := deepSeekStripDSML("I'll check that.\n" + liveDSMLAnswer)
	if strings.Contains(got, "DSML") {
		t.Errorf("DSML survived into user-visible content:\n%s", got)
	}
	if got != "I'll check that." {
		t.Errorf("prose around the block was lost:\n%s", got)
	}
}

// An answer with no DSML is returned untouched — the strip must not touch prose.
func TestDeepSeekStripDSMLLeavesPlainText(t *testing.T) {
	const answer = "Halo! Ada yang bisa dibantu?"
	if got := deepSeekStripDSML(answer); got != answer {
		t.Errorf("plain text was altered: %q", got)
	}
}

// Attributes are matched whole: a longer attribute name ending in `name` must not
// be read as the tool name.
func TestDeepSeekDSMLAttributeMatchesWholeName(t *testing.T) {
	attrs := ` toolname="wrong" name="right"`
	if got := deepSeekDSMLAttribute(attrs, "name"); got != "right" {
		t.Errorf("name = %q, want right", got)
	}
}

// A block the model was cut off partway through writing has no closing marker.
// It must not be mistaken for something to parse, and must not panic.
func TestDeepSeekDSMLTruncatedBlockIsSafe(t *testing.T) {
	truncated := "<｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"read\">\n" +
		"<｜｜DSML｜｜ parameter name=\"path\" string=\"true\">half"
	if _, ok := extractOne(truncated, []deepSeekToolSpec{readTool()}); ok {
		t.Error("a truncated block must not be parsed as a call")
	}
	if got := deepSeekStripDSML(truncated); got != "" {
		t.Errorf("a truncated block left content behind: %q", got)
	}
}

// DSML must not shadow the JSON path: an answer with no markers is still read as
// JSON, and an answer with markers is read as DSML.
func TestDeepSeekJSONPathUnaffectedByDSML(t *testing.T) {
	call, ok := extractOne(
		`{"name":"read","arguments":{"path":"/tmp/x"}}`,
		[]deepSeekToolSpec{readTool()})
	if !ok {
		t.Fatal("the JSON path stopped working")
	}
	if !strings.Contains(call.Arguments, "/tmp/x") {
		t.Errorf("arguments = %s, want the path", call.Arguments)
	}
}

// Both calls in the live answer are recoverable, in order. The second one is
// reachable only after the first is consumed, so the splitter must not stop at
// the first match.
func TestDeepSeekDSMLReadsEveryInvoke(t *testing.T) {
	invokes := deepSeekDSMLInvokes(liveDSMLAnswer)
	if len(invokes) != 2 {
		t.Fatalf("found %d invokes, want 2", len(invokes))
	}
	if !strings.Contains(invokes[0], "Jakarta") {
		t.Errorf("first invoke is not the Jakarta one:\n%s", invokes[0])
	}
	if !strings.Contains(invokes[1], "Tokyo") {
		t.Errorf("second invoke is not the Tokyo one:\n%s", invokes[1])
	}
}

// extractOne runs the real extraction path and returns the first call, for the
// many tests that assert on a single one.
func extractOne(text string, tools []deepSeekToolSpec) (deepSeekToolRequest, bool) {
	calls := deepSeekExtractToolCalls(text, tools)
	if len(calls) == 0 {
		return deepSeekToolRequest{}, false
	}
	return calls[0], true
}

// twoCityAnswer is the shape a live model produced for "check the weather in
// Jakarta and Tokyo": two invokes inside one calls element. The wrapper is the
// part that leaked — the parser consumed the invokes and handed the `calls` tags
// to the client as assistant content, so every turn showed the user an empty pair
// of tags under their own question.
const twoCityAnswer = "<\uFF5C\uFF5CDSML\uFF5C\uFF5C calls>\n" +
	"<\uFF5C\uFF5CDSML\uFF5C\uFF5C invoke name=\"read\">\n" +
	"<\uFF5C\uFF5CDSML\uFF5C\uFF5C parameter name=\"path\" string=\"true\">https://wttr.in/Jakarta?format=j1</\uFF5C\uFF5CDSML\uFF5C\uFF5C parameter>\n" +
	"</\uFF5C\uFF5CDSML\uFF5C\uFF5C invoke>\n" +
	"<\uFF5C\uFF5CDSML\uFF5C\uFF5C invoke name=\"read\">\n" +
	"<\uFF5C\uFF5CDSML\uFF5C\uFF5C parameter name=\"path\" string=\"true\">https://wttr.in/Tokyo?format=j1</\uFF5C\uFF5CDSML\uFF5C\uFF5C parameter>\n" +
	"</\uFF5C\uFF5CDSML\uFF5C\uFF5C invoke>\n" +
	"</\uFF5C\uFF5CDSML\uFF5C\uFF5C calls>"

// TestDeepSeekDSMLPreambleHasNoWrapper is the regression for the leak the user
// reported twice: the calls element that encloses the invokes must not survive
// into the assistant's content.
func TestDeepSeekDSMLPreambleHasNoWrapper(t *testing.T) {
	calls := deepSeekExtractToolCalls(twoCityAnswer, []deepSeekToolSpec{readTool()})
	if len(calls) != 2 {
		t.Fatalf("extracted %d calls, want 2", len(calls))
	}

	raws := make([]string, 0, len(calls))
	for _, call := range calls {
		raws = append(raws, call.Raw)
	}
	preamble := deepSeekSurroundingText(twoCityAnswer, raws...)

	if strings.Contains(preamble, "DSML") {
		t.Fatalf("the calls wrapper leaked as content: %q", preamble)
	}
	if preamble != "" {
		t.Fatalf("preamble = %q, want empty: the model wrote no prose around its calls", preamble)
	}
}

// TestDeepSeekDSMLReadsEveryInvokeInOneAnswer pins the multi-call behaviour: a
// model answering about two cities in one reply must not cost two round trips.
func TestDeepSeekDSMLReadsEveryInvokeInOneAnswer(t *testing.T) {
	calls := deepSeekExtractToolCalls(twoCityAnswer, []deepSeekToolSpec{readTool()})
	if len(calls) != 2 {
		t.Fatalf("extracted %d calls, want 2", len(calls))
	}
	for i, want := range []string{"Jakarta", "Tokyo"} {
		if !strings.Contains(calls[i].Arguments, want) {
			t.Errorf("call %d arguments = %s, want it to mention %s", i, calls[i].Arguments, want)
		}
	}
}

// TestDeepSeekDSMLRepeatedInvokeRunsOnce keeps a model that writes the same call
// twice from having the client execute it twice.
func TestDeepSeekDSMLRepeatedInvokeRunsOnce(t *testing.T) {
	answer := twoCityAnswer + "\n" + twoCityAnswer
	calls := deepSeekExtractToolCalls(answer, []deepSeekToolSpec{readTool()})
	if len(calls) != 2 {
		t.Fatalf("extracted %d calls, want 2 after de-duplication", len(calls))
	}
}

// TestDeepSeekDSMLStillKeepsProseAroundTheBlock guards the strip: text the model
// wrote outside a calls element is content and must survive.
func TestDeepSeekDSMLStillKeepsProseAroundTheBlock(t *testing.T) {
	answer := "Let me look at both cities.\n" + twoCityAnswer + "\nThen I will summarise."
	calls := deepSeekExtractToolCalls(answer, []deepSeekToolSpec{readTool()})

	raws := make([]string, 0, len(calls))
	for _, call := range calls {
		raws = append(raws, call.Raw)
	}
	preamble := deepSeekSurroundingText(answer, raws...)

	if strings.Contains(preamble, "DSML") {
		t.Fatalf("markup leaked: %q", preamble)
	}
	if !strings.Contains(preamble, "Let me look at both cities.") ||
		!strings.Contains(preamble, "Then I will summarise.") {
		t.Fatalf("prose around the block was lost: %q", preamble)
	}
}

// twoObjectAnswer is the shape a live model produced instead of DSML: two JSON
// call objects in one answer, one per city. The gateway returned the first and the
// second stayed in the content, so the user was shown a bare JSON fragment.
const twoObjectAnswer = "Let me check both cities.\n\n" +
	`{"name": "read", "arguments": {"path": "https://wttr.in/Jakarta?format=j1"}}` + "\n" +
	`{"name": "read", "arguments": {"path": "https://wttr.in/Tokyo?format=j1"}}`

// TestDeepSeekJSONReadsEveryObjectInOneAnswer pins the same plural rule for the
// JSON dialect that the DSML dialect already follows.
func TestDeepSeekJSONReadsEveryObjectInOneAnswer(t *testing.T) {
	calls := deepSeekExtractToolCalls(twoObjectAnswer, []deepSeekToolSpec{readTool()})
	if len(calls) != 2 {
		t.Fatalf("extracted %d calls, want 2", len(calls))
	}
	for i, want := range []string{"Jakarta", "Tokyo"} {
		if !strings.Contains(calls[i].Arguments, want) {
			t.Errorf("call %d arguments = %s, want it to mention %s", i, calls[i].Arguments, want)
		}
	}
}

// TestDeepSeekJSONSecondObjectDoesNotLeak is the user-visible half: no JSON call
// object may survive into what the assistant says.
func TestDeepSeekJSONSecondObjectDoesNotLeak(t *testing.T) {
	calls := deepSeekExtractToolCalls(twoObjectAnswer, []deepSeekToolSpec{readTool()})
	raws := make([]string, 0, len(calls))
	for _, call := range calls {
		raws = append(raws, call.Raw)
	}
	preamble := deepSeekSurroundingText(twoObjectAnswer, raws...)

	if strings.Contains(preamble, `"arguments"`) {
		t.Fatalf("a call object leaked as content: %q", preamble)
	}
	if !strings.Contains(preamble, "Let me check both cities.") {
		t.Fatalf("the model's prose was lost: %q", preamble)
	}
}

// TestDeepSeekNestedObjectIsNotACall keeps a call whose arguments hold an object
// from being reported as two calls.
func TestDeepSeekNestedObjectIsNotACall(t *testing.T) {
	answer := `{"name": "write", "arguments": {"path": "a.txt", "content": "{\"a\": 1}"}}`
	var write deepSeekToolSpec
	write.Function.Name = "write"
	calls := deepSeekExtractToolCalls(answer, []deepSeekToolSpec{write})
	if len(calls) != 1 {
		t.Fatalf("extracted %d calls, want 1: a nested object is part of its parent", len(calls))
	}
}

// leakedCronjobBlock is a DSML answer copied from a cronjob report that reached
// a Telegram group as literal text. It is malformed: the calls wrapper's opening
// tag is missing and an invoke carries no name attribute, so nothing in it can
// become a tool call. The old single-pass strip removed one element and left the
// stray parameter opener and calls closer behind.
const leakedCronjobBlock = "<｜｜DSML｜｜ parameter>\n" +
	"<｜｜DSML｜｜ invoke>\n" +
	"<｜｜DSML｜｜ parameter name=\"command\" string=\"true\">cd /tmp/rev-651 && sed -n 1060,1130p internal/handlers/chat/fallback.go</｜｜DSML｜｜ parameter>\n" +
	"</｜｜DSML｜｜ invoke>\n" +
	"</｜｜DSML｜｜ calls>"

// leakedCronjobMixedClosers is the second leaked shape: one invoke opener with
// two parameter bodies and two invoke closers, closing with a calls closer. The
// extra closer unbalances the nesting, so a scrubber that counted only one
// element kind would leave a stray tag in the output.
const leakedCronjobMixedClosers = "<｜｜DSML｜｜ calls>\n" +
	"<｜‖DSML‖｜ invoke name=\"terminal\">\n" +
	"<｜｜DSML｜｜ parameter name=\"command\" string=\"true\">cd /tmp/rev-663 && sed -n 600,700p internal/handlers/chat/models_list.go</｜｜DSML｜｜ parameter>\n" +
	"</｜｜DSML｜｜ invoke>\n" +
	"<｜｜DSML｜｜ parameter name=\"command\" string=\"true\">cd /tmp/rev-663 && sed -n 730,790p internal/handlers/chat/models_list.go</｜｜DSML｜｜ parameter>\n" +
	"</｜｜DSML｜｜ invoke>\n" +
	"</｜｜DSML｜｜ calls>"

// TestDeepSeekStripDSMLRemovesRaggedBlocks pins the two shapes that reached a
// chat as literal markup: neither is a parseable tool call, and both must be
// removed whole — tags, bodies, and orphan stray closers — with the prose
// around them intact.
func TestDeepSeekStripDSMLRemovesRaggedBlocks(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "missing calls opener", text: leakedCronjobBlock},
		{name: "unbalanced closers", text: leakedCronjobMixedClosers},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			answer := "Saya cek dulu.\n" + tt.text + "\nIni hasilnya."
			got := deepSeekStripDSML(answer)
			if strings.Contains(got, "DSML") {
				t.Fatalf("markup survived:\n%s", got)
			}
			if !strings.Contains(got, "Saya cek dulu.") || !strings.Contains(got, "Ini hasilnya.") {
				t.Fatalf("prose around the block was lost:\n%q", got)
			}
		})
	}
}

// TestDeepSeekScrubberDropsBlockAcrossFrames feeds a DSML block one fragment at
// a time, the way the streaming executor sees it. The scrubber must emit only
// the prose and keep nothing of the tags, even though every tag boundary falls
// inside a fragment.
func TestDeepSeekScrubberDropsBlockAcrossFrames(t *testing.T) {
	answer := "Baik, saya cek dulu.\n" + leakedCronjobBlock + "\nIni hasilnya."
	parts := []string{
		answer[:20],
		answer[20:40],
		answer[40:80],
		answer[80:],
	}

	var scrubber deepSeekDSMLScrubber
	var out strings.Builder
	var buf string
	for _, part := range parts {
		buf += part
		clean, n := scrubber.scrub(buf)
		out.WriteString(clean)
		buf = buf[n:]
	}
	if buf != "" {
		t.Fatalf("%d bytes were never consumed", len(buf))
	}
	got := out.String()
	if strings.Contains(got, "DSML") {
		t.Fatalf("markup survived:\n%s", got)
	}
	if !strings.Contains(got, "Baik, saya cek dulu.") || !strings.Contains(got, "Ini hasilnya.") {
		t.Fatalf("prose around the block was lost:\n%q", got)
	}
}

// TestDeepSeekScrubberHoldsPartialTagFrames feeds the same block one rune at
// a time. The upstream stream often delivers one character per frame, so a
// tag arrives as `<`, then `<｜`, then `<｜｜` — the scrubber must hold those
// prefixes instead of emitting them, or the first bytes of the markup leak as
// text.
func TestDeepSeekScrubberHoldsPartialTagFrames(t *testing.T) {
	answer := "Awal\n" + leakedCronjobBlock + "\nAkhir"
	var scrubber deepSeekDSMLScrubber
	var out strings.Builder
	var buf string
	for _, r := range answer {
		buf += string(r)
		clean, n := scrubber.scrub(buf)
		out.WriteString(clean)
		buf = buf[n:]
	}
	if buf != "" {
		t.Fatalf("%d bytes were never consumed", len(buf))
	}
	if got := out.String(); got != "Awal\n\nAkhir" {
		t.Fatalf("scrubbed = %q, want %q", got, "Awal\n\nAkhir")
	}
}
