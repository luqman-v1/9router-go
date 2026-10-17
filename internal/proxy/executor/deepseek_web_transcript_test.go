package executor

import (
	"strings"
	"testing"

	json "encoding/json/v2"
)

// realLeakedTurn is the assistant content a live omp request carried, copied out
// of requestDetails row 1792059477165. The gateway had passed the block through
// as content, omp sent it back as history, and the model then read its own markup
// as the way this conversation calls tools.
const realLeakedTurn = "<\uFF5C\uFF5CDSML\uFF5C\uFF5C calls>\n" +
	"<\uFF5C\uFF5CDSML\uFF5C\uFF5C invoke name=\"read\">\n" +
	"<\uFF5C\uFF5CDSML\uFF5C\uFF5C parameter name=\"path\" string=\"true\">" +
	"https://wttr.in/Tokyo?format=j1</\uFF5C\uFF5CDSML\uFF5C\uFF5C parameter>\n" +
	"<\uFF5C\uFF5CDSML\uFF5C\uFF5C parameter name=\"i\" string=\"true\">" +
	"Fetching Tokyo weather JSON</\uFF5C\uFF5CDSML\uFF5C\uFF5C parameter>\n" +
	"</\uFF5C\uFF5CDSML\uFF5C\uFF5C invoke>\n" +
	"</\uFF5C\uFF5CDSML\uFF5C\uFF5C calls>"

func quoted(t *testing.T, text string) []byte {
	t.Helper()
	encoded, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("marshal %q: %v", text, err)
	}
	return encoded
}

func userTurn(t *testing.T, content string) deepSeekMessage {
	t.Helper()
	return deepSeekMessage{Role: "user", Content: quoted(t, content)}
}

func assistantTurn(t *testing.T, content string) deepSeekMessage {
	t.Helper()
	return deepSeekMessage{Role: "assistant", Content: quoted(t, content)}
}

func toolTurn(t *testing.T, content string) deepSeekMessage {
	t.Helper()
	return deepSeekMessage{
		Role:       "tool",
		Content:    quoted(t, content),
		ToolCallID: "call_1",
		Name:       "read",
	}
}

// TestDeepSeekTranscriptDoesNotReplayLeakedMarkup is the regression that matters:
// a leaked block the client sends back must not reach the model.
func TestDeepSeekTranscriptDoesNotReplayLeakedMarkup(t *testing.T) {
	messages := []deepSeekMessage{
		userTurn(t, "bisa kamu cek cuaca hari ini di jakarta dan tokyo"),
		assistantTurn(t, realLeakedTurn),
		toolTurn(t, "Jakarta: 32C"),
	}

	prompt := deepSeekMessagesToPrompt(messages, 0, false)

	if strings.Contains(prompt, "DSML") {
		t.Fatalf("the transcript replayed the leaked markup:\n%s", prompt)
	}
	if !strings.Contains(prompt, "[called read]") {
		t.Fatalf("the turn was dropped instead of recorded as a call:\n%s", prompt)
	}
}

// TestDeepSeekTranscriptDropsReplayedJSONCall covers the other dialect: a JSON
// call object that reached the client as content is equally misleading.
func TestDeepSeekTranscriptDropsReplayedJSONCall(t *testing.T) {
	leaked := `Sure, let me look that up. {"name": "read", "arguments": {"path": "https://wttr.in/Jakarta"}}`
	messages := []deepSeekMessage{
		userTurn(t, "cuaca?"),
		assistantTurn(t, leaked),
		toolTurn(t, "32C"),
	}

	prompt := deepSeekMessagesToPrompt(messages, 0, false)

	if strings.Contains(prompt, `"arguments"`) {
		t.Fatalf("the transcript replayed a JSON call object:\n%s", prompt)
	}
	if !strings.Contains(prompt, "let me look that up") {
		t.Fatalf("the prose around the object was lost:\n%s", prompt)
	}
}

// TestDeepSeekTranscriptKeepsOrdinaryProse guards the repair: a normal assistant
// turn is untouched.
func TestDeepSeekTranscriptKeepsOrdinaryProse(t *testing.T) {
	messages := []deepSeekMessage{
		userTurn(t, "halo"),
		assistantTurn(t, "Halo! Ada yang bisa saya bantu?"),
	}

	prompt := deepSeekMessagesToPrompt(messages, 0, false)

	if !strings.Contains(prompt, "Ada yang bisa saya bantu?") {
		t.Fatalf("ordinary prose was dropped:\n%s", prompt)
	}
}

// TestDeepSeekTranscriptKeepsNonCallJSON keeps prose that merely contains JSON:
// an object is only removed when it is shaped like a call.
func TestDeepSeekTranscriptKeepsNonCallJSON(t *testing.T) {
	messages := []deepSeekMessage{
		userTurn(t, "apa itu JSON?"),
		assistantTurn(t, `Contohnya {"name": "Bima", "kota": "Jakarta"}`),
	}

	prompt := deepSeekMessagesToPrompt(messages, 0, false)

	if !strings.Contains(prompt, `"kota"`) {
		t.Fatalf("a non-call JSON fragment was removed:\n%s", prompt)
	}
}

// TestDeepSeekTranscriptRendersRecordedCall keeps the original behaviour intact:
// when the gateway did return a call, the transcript reads it back.
func TestDeepSeekTranscriptRendersRecordedCall(t *testing.T) {
	call := jsontextValue(t, `[{"id":"call_1","type":"function","function":`+
		`{"name":"read","arguments":"{\"path\":\"https://wttr.in/Jakarta\"}"}}]`)
	messages := []deepSeekMessage{
		userTurn(t, "cuaca?"),
		{Role: "assistant", Content: quoted(t, ""), ToolCalls: call},
		toolTurn(t, "32C"),
	}

	prompt := deepSeekMessagesToPrompt(messages, 0, false)

	if !strings.Contains(prompt, `[called read with {"path":"https://wttr.in/Jakarta"}]`) {
		t.Fatalf("the recorded call was not rendered:\n%s", prompt)
	}
}

// TestDeepSeekTranscriptKeepsProseBeforeMarkup keeps what the model wrote around
// a leaked block.
func TestDeepSeekTranscriptKeepsProseBeforeMarkup(t *testing.T) {
	messages := []deepSeekMessage{
		userTurn(t, "cuaca?"),
		assistantTurn(t, "Let me check that.\n"+realLeakedTurn),
	}

	prompt := deepSeekMessagesToPrompt(messages, 0, false)

	if strings.Contains(prompt, "DSML") {
		t.Fatalf("markup survived:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Let me check that.") {
		t.Fatalf("prose before the block was lost:\n%s", prompt)
	}
}

func jsontextValue(t *testing.T, text string) []byte {
	t.Helper()
	if !deepSeekValidJSON(text) {
		t.Fatalf("fixture is not valid JSON: %s", text)
	}
	return []byte(text)
}
