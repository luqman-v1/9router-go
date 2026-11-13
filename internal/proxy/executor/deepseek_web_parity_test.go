package executor

// Parity tests against upstream decolua/9router (open-sse/executors/deepseek-web
// and open-sse/translator/deepseekWebToolBinding.js). Each test names the
// upstream behaviour it pins.

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- citation format -------------------------------------------------------

func citationAccumulator(model string, cites ...deepSeekSearchResult) *deepSeekStreamAccumulator {
	a := newDeepSeekStreamAccumulator(model)
	a.cites = cites
	return a
}

// TestDeepSeekCitationsUseUpstreamFormat pins appendSearchCitations: the block
// is `[N]: [title](url)` lines in ascending cite order, preceded by a blank line.
func TestDeepSeekCitationsUseUpstreamFormat(t *testing.T) {
	a := citationAccumulator("deepseek-search",
		deepSeekSearchResult{Title: "Second", URL: "https://b.example", CiteIndex: 2},
		deepSeekSearchResult{Title: "First", URL: "https://a.example", CiteIndex: 1},
	)
	got := a.contentText()
	want := "\n\n[1]: [First](https://a.example)\n[2]: [Second](https://b.example)"
	if !strings.HasSuffix(got, want) {
		t.Fatalf("citations = %q, want it to end with %q", got, want)
	}
}

// TestDeepSeekCitationsFilteredByCiteIndex pins the `filter(r => r.cite_index)`
// upstream applies: a result the stream never numbered is not rendered, and
// neither is index 0.
func TestDeepSeekCitationsFilteredByCiteIndex(t *testing.T) {
	a := citationAccumulator("deepseek-search",
		deepSeekSearchResult{Title: "Numbered", URL: "https://n.example", CiteIndex: 1},
		deepSeekSearchResult{Title: "Unnumbered", URL: "https://u.example", CiteIndex: 0},
	)
	got := a.contentText()
	if strings.Contains(got, "Unnumbered") {
		t.Fatalf("a result without a cite_index reached the answer: %q", got)
	}
	if !strings.Contains(got, "[1]: [Numbered](https://n.example)") {
		t.Fatalf("the numbered citation was dropped: %q", got)
	}
}

// TestDeepSeekCitationsEmptyWhenNoCites keeps a plain answer free of a dangling
// blank block.
func TestDeepSeekCitationsEmptyWhenNoCites(t *testing.T) {
	a := newDeepSeekStreamAccumulator("deepseek-search")
	if got := a.contentText(); got != "" {
		t.Fatalf("contentText = %q, want empty", got)
	}
}

// TestDeepSeekSearchSilentHasNoCitationsAndNoMarkers pins the search-silent
// dialect: the model still writes [citation:N], the block never renders, and the
// markers are removed rather than rewritten.
func TestDeepSeekSearchSilentHasNoCitationsAndNoMarkers(t *testing.T) {
	a := citationAccumulator("deepseek-search-silent",
		deepSeekSearchResult{Title: "X", URL: "https://x.example", CiteIndex: 1},
	)
	a.append("According to [citation:1], done.")
	got := a.contentText()
	if strings.Contains(got, "[citation:") {
		t.Fatalf("a citation marker survived: %q", got)
	}
	if strings.Contains(got, "https://x.example") {
		t.Fatalf("search-silent rendered the citation block: %q", got)
	}
	if got != "According to , done." {
		t.Fatalf("content = %q, want the marker removed in place", got)
	}
}

// TestDeepSeekSearchModelRewritesCitationMarkers pins formatStreamContent for a
// search model: `[citation:N]` becomes `[N]`.
func TestDeepSeekSearchModelRewritesCitationMarkers(t *testing.T) {
	a := newDeepSeekStreamAccumulator("deepseek-search")
	a.append("Source [citation:3] says.")
	if got := a.contentText(); got != "Source [3] says." {
		t.Fatalf("content = %q, want the marker rewritten to [3]", got)
	}
}

// TestDeepSeekNonSearchModelKeepsMarkers pins the other half: a model id that
// is not a search model gets no rewriting at all.
func TestDeepSeekNonSearchModelKeepsMarkers(t *testing.T) {
	a := newDeepSeekStreamAccumulator("deepseek-v4-pro")
	a.append("Source [citation:3] says.")
	if got := a.contentText(); got != "Source [citation:3] says." {
		t.Fatalf("content = %q, want the marker untouched on a non-search model", got)
	}
}

// TestDeepSeekFoldModelIsASearchModel pins isSearchModel's "fold" branch: the
// fold ids are search models without the word in them.
func TestDeepSeekFoldModelIsASearchModel(t *testing.T) {
	a := newDeepSeekStreamAccumulator("deepseek-fold-1")
	a.append("Source [citation:2] says.")
	if got := a.contentText(); got != "Source [2] says." {
		t.Fatalf("content = %q, want fold treated as a search model", got)
	}
}

// TestDeepSeekCleanTokenStripsTransport pins cleanDeepSeekToken: FINISHED is
// removed wherever it appears and a SEARCH opening marker does not reach the
// answer.
func TestDeepSeekCleanTokenStripsTransport(t *testing.T) {
	a := newDeepSeekStreamAccumulator("deepseek-search")
	a.append("SEARCH Jakarta weather")
	a.append(" FINISHED")
	if got := a.contentText(); got != "Jakarta weather " {
		t.Fatalf("content = %q, want the transport tokens stripped", got)
	}
}

// --- prompt: markdown images ----------------------------------------------

// TestDeepSeekPromptStripsMarkdownImages pins messagesToPrompt's
// `replace(/!\[.*?\]\(.*?\)/g, "")`: the format has no image slot, and a model
// reading the syntax as content answers about the alt text.
func TestDeepSeekPromptStripsMarkdownImages(t *testing.T) {
	messages := []deepSeekMessage{
		{Role: "user", Content: jsontext.Value(`"Look at ![chart](https://x/y.png) and tell me."`)},
	}
	prompt := deepSeekMessagesToPrompt(messages, 0, false)
	if strings.Contains(prompt, "![chart]") || strings.Contains(prompt, "https://x/y.png") {
		t.Fatalf("an image reference survived the prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Look at  and tell me.") {
		t.Fatalf("the text around the image was lost:\n%s", prompt)
	}
}

// TestDeepSeekTranscriptPromptStripsMarkdownImages covers the transcript return,
// which is the path a persisted session takes.
func TestDeepSeekTranscriptPromptStripsMarkdownImages(t *testing.T) {
	messages := []deepSeekMessage{
		{Role: "user", Content: jsontext.Value(`"first: ![a](https://a/1.png)"`)},
		{Role: "assistant", Content: jsontext.Value(`"noted"`)},
		{Role: "user", Content: jsontext.Value(`"second: ![b](https://b/2.png)"`)},
	}
	prompt := deepSeekMessagesToPrompt(messages, 0, true)
	if strings.Contains(prompt, "![") {
		t.Fatalf("an image reference survived the transcript:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Assistant: noted") {
		t.Fatalf("the transcript was lost:\n%s", prompt)
	}
}

// --- nonce binding ---------------------------------------------------------

func bindingTools() []deepSeekToolSpec {
	var tool deepSeekToolSpec
	tool.Function.Name = "get_weather"
	tool.Function.Parameters = jsontext.Value(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)
	return []deepSeekToolSpec{tool}
}

// TestDeepSeekToolNonceShapes pins getToolNonce's contract: eight characters
// drawn from the 0-9a-z alphabet, empty for a request with no tools, and a
// different value per mint.
func TestDeepSeekToolNonceShapes(t *testing.T) {
	if nonce := deepSeekToolNonce(nil); nonce != "" {
		t.Fatalf("a request with no tools must have no nonce, got %q", nonce)
	}
	nonce := deepSeekToolNonce(bindingTools())
	if len(nonce) != 8 {
		t.Fatalf("nonce = %q, want 8 characters", nonce)
	}
	for _, c := range nonce {
		if !strings.ContainsRune(deepSeekNonceAlphabet, c) {
			t.Fatalf("nonce %q carries %q, outside the alphabet", nonce, c)
		}
	}
	if again := deepSeekToolNonce(bindingTools()); again == nonce {
		t.Fatalf("a second mint produced %q again; the binding must be per request", nonce)
	}
}

// TestDeepSeekBoundNonceCallIsAcceptedAndStripped is the happy path: a call
// carrying the request's `_nonce` comes back with the binding removed, so the
// client never sees an argument it did not declare.
func TestDeepSeekBoundNonceCallIsAcceptedAndStripped(t *testing.T) {
	nonce := deepSeekToolNonce(bindingTools())
	answer := `{"name":"get_weather","arguments":{"city":"Jakarta","_nonce":"` + nonce + `"}}`
	calls := deepSeekExtractToolCalls(answer, bindingTools(), nonce)
	if len(calls) != 1 {
		t.Fatalf("a bound call was rejected: %d calls", len(calls))
	}
	if strings.Contains(calls[0].Arguments, "_nonce") {
		t.Fatalf("the binding leaked to the client: %s", calls[0].Arguments)
	}
	if !strings.Contains(calls[0].Arguments, "Jakarta") {
		t.Fatalf("arguments were lost stripping the binding: %s", calls[0].Arguments)
	}
}

// TestDeepSeekWrongNonceRejectsTheWholeAnswer pins the all-or-nothing rule: one
// envelope with a foreign nonce turns the whole answer into content, because a
// client that ran the surviving calls would report the answer as complete.
func TestDeepSeekWrongNonceRejectsTheWholeAnswer(t *testing.T) {
	nonce := deepSeekToolNonce(bindingTools())
	answer := `{"name":"get_weather","arguments":{"city":"Jakarta","_nonce":"` + nonce + `"}}` +
		"\n" +
		`{"name":"get_weather","arguments":{"city":"Tokyo","_nonce":"stolen"}}`
	calls := deepSeekExtractToolCalls(answer, bindingTools(), nonce)
	if len(calls) != 0 {
		t.Fatalf("an answer carrying a foreign nonce produced %d calls", len(calls))
	}
}

// TestDeepSeekSchemaViolationRejectsTheBoundCall pins validateToolArguments: a
// call that satisfies the nonce but violates the declared schema is rejected.
func TestDeepSeekSchemaViolationRejectsTheBoundCall(t *testing.T) {
	nonce := deepSeekToolNonce(bindingTools())
	answer := `{"name":"get_weather","arguments":{"city":42,"_nonce":"` + nonce + `"}}`
	if calls := deepSeekExtractToolCalls(answer, bindingTools(), nonce); len(calls) != 0 {
		t.Fatalf("a schema-violating call was accepted: %v", calls)
	}
}

// TestDeepSeekUnboundCallStillAccepted pins upstream's backward compatibility:
// an answer without any binding is parsed as before — the prompt asks for the
// nonce, but a model that omits it is not rejected outright.
func TestDeepSeekUnboundCallStillAccepted(t *testing.T) {
	answer := `{"name":"get_weather","arguments":{"city":"Jakarta"}}`
	calls := deepSeekExtractToolCalls(answer, bindingTools(), "anonce")
	if len(calls) != 1 {
		t.Fatalf("a call without a binding was rejected: %d calls", len(calls))
	}
	if !deepSeekValidateToolArguments(bindingTools(), "get_weather", map[string]any{"city": "Jakarta"}) {
		t.Fatal("valid arguments rejected against the declared schema")
	}
	if deepSeekValidateToolArguments(bindingTools(), "get_weather", map[string]any{"city": 42}) {
		t.Fatal("a wrong-typed argument validated against the declared schema")
	}
}

// TestDeepSeekDSMLNonceBindingIsEnforced covers the DSML dialect: an invoke
// whose `_nonce` parameter disagrees with the request is not a call, and the
// binding never reaches the client's arguments.
func TestDeepSeekDSMLNonceBindingIsEnforced(t *testing.T) {
	nonce := deepSeekToolNonce(bindingTools())
	invoke := func(value string) string {
		answer := `<｜｜DSML｜｜ invoke name="get_weather">` +
			`<｜｜DSML｜｜ parameter name="city" string="true">Jakarta</｜｜DSML｜｜ parameter>`
		if value != "" {
			answer += `<｜｜DSML｜｜ parameter name="_nonce" string="true">` + value + `</｜｜DSML｜｜ parameter>`
		}
		return answer + `</｜｜DSML｜｜ invoke>`
	}

	if calls := deepSeekExtractToolCalls(invoke("other"), bindingTools(), nonce); len(calls) != 0 {
		t.Fatalf("a DSML invoke with a foreign binding produced %d calls", len(calls))
	}
	calls := deepSeekExtractToolCalls(invoke(nonce), bindingTools(), nonce)
	if len(calls) != 1 {
		t.Fatalf("a bound DSML invoke was rejected: %d calls", len(calls))
	}
	if strings.Contains(calls[0].Arguments, "_nonce") {
		t.Fatalf("the DSML binding leaked to the client: %s", calls[0].Arguments)
	}
}

// TestDeepSeekDSMLArgumentsAreSchemaValidated pins the DSML contract, which
// upstream always schema-validates: an invoke whose argument violates the
// declared schema is not a call.
func TestDeepSeekDSMLArgumentsAreSchemaValidated(t *testing.T) {
	tools := bindingTools()
	tools[0].Function.Parameters = jsontext.Value(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`)
	answer := `<｜｜DSML｜｜ invoke name="get_weather">` +
		`<｜｜DSML｜｜ parameter name="town" string="true">Jakarta</｜｜DSML｜｜ parameter>` +
		`</｜｜DSML｜｜ invoke>`
	if calls := deepSeekExtractToolCalls(answer, tools, ""); len(calls) != 0 {
		t.Fatalf("a DSML call violating its schema produced %d calls", len(calls))
	}
}

// --- malformed-envelope detection + repair ---------------------------------

// TestDeepSeekMalformedMarkupDetection pins hasMalformedDeepSeekToolMarkup:
// raw DSML and bare <tool> wrappers are the shapes the repair retry answers to.
func TestDeepSeekMalformedMarkupDetection(t *testing.T) {
	markup := []string{
		`<｜｜DSML｜｜ invoke name="get_weather"><｜｜DSML｜｜ parameter name="city">X</｜｜DSML｜｜ parameter></｜｜DSML｜｜ invoke>`,
		`<tool>{"name":"get_weather","arguments":{"city":"X"}}</tool>`,
		`<tool_call>{"name":"get_weather"}</tool_call>`,
		`<|DSML|invoke name="get_weather">`,
	}
	for _, text := range markup {
		if !deepSeekHasMalformedToolMarkup(text) {
			t.Errorf("markup not detected: %q", text)
		}
	}
	plain := []string{
		"The weather in Jakarta is 31C.",
		`{"name":"get_weather","arguments":{"city":"Jakarta"}}`,
		"",
	}
	for _, text := range plain {
		if deepSeekHasMalformedToolMarkup(text) {
			t.Errorf("plain text flagged as markup: %q", text)
		}
	}
}

// TestDeepSeekMalformedIntentDetection pins hasMalformedDeepSeekToolIntent: a
// JSON object naming a declared tool with an arguments field is intent even
// though it parsed, and prose naming the tool is not.
func TestDeepSeekMalformedIntentDetection(t *testing.T) {
	tools := bindingTools()
	if !deepSeekHasMalformedToolIntent(`{"command":"get_weather","arguments":{"city":"X"}}`, tools) {
		t.Fatal("an envelope with a arguments field and a declared name was not flagged")
	}
	if deepSeekHasMalformedToolIntent("I would call get_weather for that.", tools) {
		t.Fatal("prose naming a tool was flagged as intent")
	}
	if deepSeekHasMalformedToolIntent(`{"name":"undeclared_tool","arguments":{}}`, tools) {
		t.Fatal("an undeclared tool name was flagged as intent")
	}
}

// TestDeepSeekRepairPromptRestatesContract keeps the repair wording anchored:
// the retry must tell the model its envelope was rejected and what a valid one
// carries.
func TestDeepSeekRepairPromptRestatesContract(t *testing.T) {
	prompt := deepSeekRepairPrompt("previous answer")
	for _, want := range []string{"previous answer", "invalid tool envelope", "_nonce binding", "argument schema"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("repair prompt missing %q:\n%s", want, prompt)
		}
	}
}

// TestDeepSeekToolTurnRepairsMalformedEnvelope drives the real tool turn with a
// reissue closure: a malformed envelope must trigger exactly one repair on the
// fresh session, and the repaired answer is what reaches the client.
func TestDeepSeekToolTurnRepairsMalformedEnvelope(t *testing.T) {
	var spec deepSeekToolSpec
	spec.Function.Name = "get_weather"
	env := deepSeekRequestEnvelope{Model: "deepseek-v4-pro", Tools: []deepSeekToolSpec{spec}}

	// A tool block whose JSON is truncated: markup that expresses intent the
	// parser cannot bind, which is exactly what the repair retry answers to.
	malformed := newSignedOffTurn(t, `<tool_call>{"name":"get_weather","arguments":{"city":"Jak`)

	req := &Request{IsStream: false, Ctx: t.Context()}
	rec := httptest.NewRecorder()

	repairs := 0
	reissue := func(string) (*deepSeekStreamAccumulator, error) {
		repairs++
		repaired := newDeepSeekStreamAccumulator(env.Model)
		if _, err := deepSeekAccumulateInto(repaired, newSignedOffTurn(t, `{"name":"get_weather","arguments":{"city":"Jakarta"}}`)); err != nil {
			return nil, err
		}
		return repaired, nil
	}

	nonce := deepSeekToolNonce(env.Tools)
	settled, err := deepSeekToolTurnToClient(rec, req, malformed, env, newDeepSeekStreamAccumulator(env.Model), nonce, reissue)
	if err != nil {
		t.Fatalf("tool turn: %v", err)
	}
	if repairs != 1 {
		t.Fatalf("the turn issued %d repairs, want exactly one", repairs)
	}
	if settled == nil {
		t.Fatal("the turn returned no accumulator; settlement would see the rejected stream")
	}
	if !strings.Contains(settled.contentText(), "Jakarta") {
		t.Fatalf("the repaired answer was lost: %q", settled.contentText())
	}

	var out struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if len(out.Choices) != 1 || len(out.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("the repaired call never reached the client:\n%s", rec.Body.String())
	}
}

// TestDeepSeekToolTurnGivesUpAfterOneBadRepair pins the single-retry rule: a
// model that answers the repair with markup again is reported as a failure, not
// repaired forever.
func TestDeepSeekToolTurnGivesUpAfterOneBadRepair(t *testing.T) {
	var spec deepSeekToolSpec
	spec.Function.Name = "get_weather"
	env := deepSeekRequestEnvelope{Model: "deepseek-v4-pro", Tools: []deepSeekToolSpec{spec}}

	req := &Request{IsStream: false, Ctx: t.Context()}
	rec := httptest.NewRecorder()

	repairs := 0
	reissue := func(string) (*deepSeekStreamAccumulator, error) {
		repairs++
		repaired := newDeepSeekStreamAccumulator(env.Model)
		if _, err := deepSeekAccumulateInto(repaired, newSignedOffTurn(t, `<tool_call>{"name":"get_weather","arguments":{"ci`)); err != nil {
			return nil, err
		}
		return repaired, nil
	}

	_, err := deepSeekToolTurnToClient(rec, req,
		newSignedOffTurn(t, `<tool_call>{"name":"get_weather","arguments":{"city":"Jak`),
		env, newDeepSeekStreamAccumulator(env.Model), deepSeekToolNonce(env.Tools), reissue)
	if err == nil {
		t.Fatal("a second malformed envelope must fail the turn, not answer as prose")
	}
	if repairs != 1 {
		t.Fatalf("issued %d repairs, want exactly one", repairs)
	}
}

// TestDeepSeekToolTurnWithoutNonceNeverRepairs keeps the plain tool path
// untouched: a request that minted no nonce has no contract to repair against.
func TestDeepSeekToolTurnWithoutNonceNeverRepairs(t *testing.T) {
	var spec deepSeekToolSpec
	spec.Function.Name = "get_weather"
	env := deepSeekRequestEnvelope{Model: "deepseek-v4-pro", Tools: []deepSeekToolSpec{spec}}

	req := &Request{IsStream: false, Ctx: t.Context()}
	rec := httptest.NewRecorder()

	reissue := func(string) (*deepSeekStreamAccumulator, error) {
		t.Fatal("a turn without a nonce must not issue a repair")
		return nil, nil
	}

	_, err := deepSeekToolTurnToClient(rec, req,
		newSignedOffTurn(t, `<tool>{"name":"get_weather","arguments":{"city":"Jakarta"}}</tool>`),
		env, newDeepSeekStreamAccumulator(env.Model), "", reissue)
	if err != nil {
		t.Fatalf("tool turn: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "get_weather") || strings.Contains(rec.Body.String(), "tool_calls") {
		// Unbound markup is passed through as content (the DSML strip covers
		// DSML; a bare <tool> wrapper is not DSML) — the pre-parity behaviour.
		t.Logf("unbound markup answer: %s", rec.Body.String())
	}
}

// TestDeepSeekStrayToolTagsDoNotLeak pins upstream's post-parse content
// filter: a `<tool>{json}</tool>` wrapper whose body was promoted leaves its
// empty shell behind, and that shell is markup rather than prose.
func TestDeepSeekStrayToolTagsDoNotLeak(t *testing.T) {
	answer := `<tool>{"name":"get_weather","arguments":{"city":"Jakarta"}}</tool>`
	tools := bindingTools()
	calls := deepSeekExtractToolCalls(answer, tools)
	if len(calls) != 1 {
		t.Fatalf("the wrapped call was not recovered: %d calls", len(calls))
	}
	preamble := deepSeekSurroundingText(answer, calls[0].Raw)
	if strings.Contains(preamble, "<tool>") || strings.Contains(preamble, "</tool>") {
		t.Fatalf("the wrapper shell leaked as content: %q", preamble)
	}
}

// --- prompt binding --------------------------------------------------------

// TestDeepSeekRenderToolsCarriesNonceBinding pins the prompt half of the
// contract: the rendered schema carries `_nonce` as a const required property
// and the instructions name it.
func TestDeepSeekRenderToolsCarriesNonceBinding(t *testing.T) {
	tools := bindingTools()
	nonce := "abc123xy"
	rendered := deepSeekRenderTools(tools, nonce)
	for _, want := range []string{`"const":"` + nonce + `"`, `"_nonce"`, nonce} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered tools missing %q:\n%s", want, rendered)
		}
	}
	// The unbound call sites must render exactly as before.
	plain := deepSeekRenderTools(tools)
	if strings.Contains(plain, "_nonce") {
		t.Fatalf("a nonceless render invented a binding:\n%s", plain)
	}
}
