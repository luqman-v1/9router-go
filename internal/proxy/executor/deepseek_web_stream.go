package executor

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strconv"
	"strings"
)

// deepSeekStreamEvent is one `data:` payload of the upstream event stream. The
// upstream is a generic pub/sub envelope: `p` names the topic, `v` carries the
// payload, and `o` marks a batched patch that rewrites entries already streamed
// instead of appending new ones.
type deepSeekStreamEvent struct {
	P string         `json:"p"`
	O string         `json:"o"`
	V jsontext.Value `json:"v"`
}

// deepSeekFragment is one streamed piece of a response.
type deepSeekFragment struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// deepSeekResponsePayload is the `v.response` shape, which carries the
// reasoning mode for everything after it, the upstream's own message id and
// the fragments themselves.
type deepSeekResponsePayload struct {
	Response struct {
		ThinkingEnabled bool               `json:"thinking_enabled"`
		MessageID       *int64             `json:"message_id"`
		Fragments       []deepSeekFragment `json:"fragments"`
	} `json:"response"`
}

// deepSeekSearchResult is one citation behind a search-enabled answer.
type deepSeekSearchResult struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Snippet   string `json:"snippet"`
	Date      string `json:"date"`
	CiteIndex int    `json:"cite_index"`
}

// deepSeekCitePatch is the batched operation that stamps a final citation index
// onto a result the stream already delivered.
type deepSeekCitePatch struct {
	P string `json:"p"`
	V int    `json:"v"`
}

// Upstream fragment types. THINK opens the reasoning channel; ANSWER and
// RESPONSE both open the visible one.
const (
	deepSeekFragmentThink    = "THINK"
	deepSeekFragmentAnswer   = "ANSWER"
	deepSeekFragmentResponse = "RESPONSE"
)

// Channel labels, used when a fragment has not named one yet.
const (
	deepSeekChannelThinking = "thinking"
	deepSeekChannelContent  = "content"
)

// deepSeekStreamAccumulator turns the upstream event stream into the text a
// client should see. One instance serves both paths: the streaming executor
// reads its deltas as they arrive, the non-streaming one reads the same state
// once the stream ends.
type deepSeekStreamAccumulator struct {
	// thinkingOnly is set for a reasoning model before any fragment has named a
	// channel, which is where upstream sends untyped fragments for those ids.
	thinkingOnly bool

	channel  string
	content  strings.Builder
	thinking strings.Builder
	cites    []deepSeekSearchResult

	sawFinished bool

	// responseMessageID is the upstream's own id for the answer it is
	// streaming. With session persistence it is the parent the next turn
	// threads on, so it is recorded as soon as the stream names it.
	responseMessageID *int64

	// enforcement is set when the upstream refused the turn inside the stream
	// instead of answering — an account-level mute or a rate-limit hint. The
	// payload carries no completion, so a turn that only ever received one has
	// to be reported as the refusal it is rather than as a cut-off session.
	enforcement *deepSeekEnforcement
}

// newDeepSeekStreamAccumulator returns an accumulator for one requested model.
func newDeepSeekStreamAccumulator(model string) *deepSeekStreamAccumulator {
	return &deepSeekStreamAccumulator{thinkingOnly: deepSeekIsThinkingModel(model)}
}

// deepSeekIsThinkingModel reports whether a requested model id asks for
// reasoning output. This upstream's catalog carries no separate wire ids, so
// the requested id is the only carrier of that choice.
func deepSeekIsThinkingModel(model string) bool {
	name := strings.ToLower(model)
	return strings.Contains(name, "r1") ||
		strings.Contains(name, "think") ||
		strings.Contains(name, "reason")
}

// consume reads one raw `data:` payload, recording a refusal before folding the
//
// The payload is read twice on purpose: the enforcement fields sit at the top
// level of an error frame, beside `p` and `v` rather than inside them, and the
// payload's shape — not the topic — is what says the upstream refused.
func (a *deepSeekStreamAccumulator) consume(payload []byte) {
	if enforcement := deepSeekEnforcementFromEvent(payload); enforcement != nil {
		a.enforcement = enforcement
	}

	var event deepSeekStreamEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return
	}
	a.apply(event)
}

// apply folds one upstream event into the accumulated state.
//
// The live stream carries three shapes the topic switch has to cover, and only
// one of them names a topic:
//
//	{"v":{"response":{...fragments...}}}                 the answer, topicless
//	{"p":"response/fragments/-1/content","o":"APPEND","v":"x"}  one character
//	{"p":"response","o":"BATCH","v":[{"p":"quasi_status","v":"FINISHED"}]}
//
// The topicless form is the common one: dispatching on `p` alone sends it to the
// default branch, where a bare string is appended but an object is dropped —
// which loses the whole answer while still finding the FINISHED marker, so a
// turn reports success with no text. The object form is therefore checked
// before the topic switch.
func (a *deepSeekStreamAccumulator) apply(event deepSeekStreamEvent) {
	if event.P == "" {
		a.applyTopicless(event.V)
		return
	}

	switch event.P {
	case "response/status":
		if s, ok := decodeJSONString(event.V); ok && s == "FINISHED" {
			a.sawFinished = true
		}
		return
	case "response/search_status":
		return
	case "response/search_results":
		a.applySearchResults(event)
		return
	case "response/fragments":
		a.applyFragments(event.V)
		return
	case "response":
		a.applyResponseTopic(event.V)
		return
	}

	// A per-character append into a fragment slot. The index is negative
	// upstream, naming a slot that does not exist yet, so the append is treated
	// as continuation of the channel already open rather than as a new one.
	if event.O == "APPEND" && strings.HasPrefix(event.P, "response/fragments/") && strings.HasSuffix(event.P, "/content") {
		if s, ok := decodeJSONString(event.V); ok {
			a.append(s)
		}
		return
	}

	// Any other topic carrying a bare string is visible text on whichever
	// channel is already open.
	if s, ok := decodeJSONString(event.V); ok {
		a.append(s)
	}
}

// applyTopicless handles the shape the answer arrives in: a bare string is text,
// and an object carrying a `response` key is a payload the topic switch never
// sees.
func (a *deepSeekStreamAccumulator) applyTopicless(value jsontext.Value) {
	if s, ok := decodeJSONString(value); ok {
		a.append(s)
		return
	}
	var payload deepSeekResponsePayload
	if err := json.Unmarshal(value, &payload); err == nil {
		a.applyResponsePayload(payload)
	}
}

// applyResponseTopic handles the "response" topic, which arrives in two shapes:
// an array of entries that announces the reasoning mode and carries fragments,
// and a single object doing the same.
func (a *deepSeekStreamAccumulator) applyResponseTopic(value jsontext.Value) {
	var entries []deepSeekResponsePayload
	if err := json.Unmarshal(value, &entries); err == nil && len(entries) > 0 {
		for _, entry := range entries {
			a.applyResponsePayload(entry)
		}
		return
	}

	var payload deepSeekResponsePayload
	if err := json.Unmarshal(value, &payload); err == nil {
		a.applyResponsePayload(payload)
	}
}

func (a *deepSeekStreamAccumulator) applyResponsePayload(payload deepSeekResponsePayload) {
	// thinking_enabled is sticky: once the upstream says reasoning is on, every
	// fragment after it belongs to the reasoning channel until one names another.
	if payload.Response.ThinkingEnabled {
		a.channel = deepSeekChannelThinking
	}
	// The upstream's own message id, which is the parent the next turn of a
	// persisted session threads on.
	if payload.Response.MessageID != nil {
		a.responseMessageID = payload.Response.MessageID
	}
	for _, fragment := range payload.Response.Fragments {
		a.appendFragment(fragment)
	}
}

// applyFragments folds a fragments array, or a single fragment object.
func (a *deepSeekStreamAccumulator) applyFragments(value jsontext.Value) {
	var fragments []deepSeekFragment
	if err := json.Unmarshal(value, &fragments); err == nil && len(fragments) > 0 {
		for _, fragment := range fragments {
			a.appendFragment(fragment)
		}
		return
	}

	var fragment deepSeekFragment
	if err := json.Unmarshal(value, &fragment); err == nil {
		a.appendFragment(fragment)
	}
}

// appendFragment folds one fragment in. A fragment's own type selects the
// channel, and that choice persists for every fragment after it.
func (a *deepSeekStreamAccumulator) appendFragment(fragment deepSeekFragment) {
	if fragment.Content == "" {
		return
	}
	switch strings.ToUpper(fragment.Type) {
	case deepSeekFragmentThink:
		a.channel = deepSeekChannelThinking
	case deepSeekFragmentAnswer, deepSeekFragmentResponse:
		a.channel = deepSeekChannelContent
	}
	a.append(fragment.Content)
}

// append routes one piece of text to the active channel.
func (a *deepSeekStreamAccumulator) append(text string) {
	switch a.channel {
	case deepSeekChannelThinking:
		a.thinking.WriteString(text)
	case deepSeekChannelContent:
		a.content.WriteString(text)
	default:
		if a.thinkingOnly {
			a.thinking.WriteString(text)
		} else {
			a.content.WriteString(text)
		}
	}
}

// applySearchResults records the citations behind a search-enabled answer, and
// folds in the batched index patches that follow to renumber them.
func (a *deepSeekStreamAccumulator) applySearchResults(event deepSeekStreamEvent) {
	if event.O == "BATCH" {
		var patches []deepSeekCitePatch
		if err := json.Unmarshal(event.V, &patches); err != nil {
			return
		}
		for _, patch := range patches {
			index, ok := deepSeekParseCiteIndex(patch.P)
			if ok && index >= 0 && index < len(a.cites) {
				a.cites[index].CiteIndex = patch.V
			}
		}
		return
	}

	var results []deepSeekSearchResult
	if err := json.Unmarshal(event.V, &results); err == nil && len(results) > 0 {
		a.cites = results
	}
}

// deepSeekParseCiteIndex reads the "<n>/cite_index" operation path a batched
// search-results patch is keyed by.
func deepSeekParseCiteIndex(path string) (int, bool) {
	const suffix = "/cite_index"
	if !strings.HasSuffix(path, suffix) {
		return 0, false
	}
	head := strings.TrimSuffix(path, suffix)
	if head == "" {
		return 0, false
	}
	index := 0
	for _, c := range head {
		if c < '0' || c > '9' {
			return 0, false
		}
		index = index*10 + int(c-'0')
	}
	return index, true
}

// citations renders the accumulated search results as a trailing block.
// Upstream appends these to the answer rather than inventing a field, because
// this upstream's clients have nowhere else to look for them.
func (a *deepSeekStreamAccumulator) citations() string {
	var b strings.Builder
	for _, cite := range a.cites {
		title := cite.Title
		if title == "" {
			title = cite.URL
		}
		if title == "" {
			continue
		}
		b.WriteString("\n[")
		b.WriteString(strconv.Itoa(cite.CiteIndex))
		b.WriteString("] ")
		b.WriteString(title)
		if cite.URL != "" && cite.URL != title {
			b.WriteString(" (")
			b.WriteString(cite.URL)
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// contentText is the visible answer with any citations appended.
func (a *deepSeekStreamAccumulator) contentText() string {
	return a.content.String() + a.citations()
}

// thinkingText is the reasoning channel, when the model produced one.
func (a *deepSeekStreamAccumulator) thinkingText() string {
	return a.thinking.String()
}

// deepSeekDeltaFrom returns the part of text that follows what the caller has
// already emitted, plus the new sent offset. The accumulator keeps the whole
// answer and the caller tracks how much of it it has sent, because contentText
// has to be readable again for the citations that trail the visible text.
func deepSeekDeltaFrom(text string, sent int) (string, int) {
	if sent >= len(text) {
		return "", len(text)
	}
	return text[sent:], len(text)
}

// decodeJSONString decodes a JSON string value, reporting false for anything
// else so a structured payload is never mistaken for text.
func decodeJSONString(value jsontext.Value) (string, bool) {
	if len(value) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(value, &s); err != nil {
		return "", false
	}
	return s, true
}
