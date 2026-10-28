package executor

import (
	"encoding/json"
	"strings"
	"testing"
)

const pngB64 = "iVBORw0KGgo="

func claudeMessagesOf(t *testing.T, in string) []any {
	t.Helper()
	out := EnsureClaudeMessagesFor([]byte(in), "claude", "claude-sonnet-5-5")
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	msgs, _ := m["messages"].([]any)
	return msgs
}

func assertNoImageURL(t *testing.T, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "image_url") {
		t.Fatalf("OpenAI image_url leaked into the Claude body: %s", b)
	}
}

func contentBlocks(t *testing.T, msg any) []any {
	t.Helper()
	blocks, ok := msg.(map[string]any)["content"].([]any)
	if !ok {
		t.Fatalf("content is not an array: %#v", msg)
	}
	return blocks
}

func assertBase64Image(t *testing.T, block any) {
	t.Helper()
	b := block.(map[string]any)
	src, _ := b["source"].(map[string]any)
	if b["type"] != "image" || src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] != pngB64 {
		t.Fatalf("not a Claude base64 image block: %#v", block)
	}
}

func TestEnsureClaudeMessages_UserImageURLBecomesImageBlock(t *testing.T) {
	msgs := claudeMessagesOf(t, `{"model":"claude-sonnet-5-5","messages":[
		{"role":"user","content":[{"type":"text","text":"what colour?"},
			{"type":"image_url","image_url":{"url":"data:image/png;base64,`+pngB64+`"}}]}]}`)
	assertNoImageURL(t, msgs)
	blocks := contentBlocks(t, msgs[0])
	if blocks[0].(map[string]any)["type"] != "text" {
		t.Fatalf("text part must survive: %#v", blocks[0])
	}
	assertBase64Image(t, blocks[1])
}

func TestEnsureClaudeMessages_RemoteImageURLBecomesURLSource(t *testing.T) {
	msgs := claudeMessagesOf(t, `{"model":"claude-sonnet-5-5","messages":[
		{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`)
	assertNoImageURL(t, msgs)
	src := contentBlocks(t, msgs[0])[0].(map[string]any)["source"].(map[string]any)
	if src["type"] != "url" || src["url"] != "https://example.com/a.png" {
		t.Fatalf("remote image must become a url source: %#v", src)
	}
}

func TestEnsureClaudeMessages_BareStringImageURL(t *testing.T) {
	msgs := claudeMessagesOf(t, `{"model":"claude-sonnet-5-5","messages":[
		{"role":"user","content":[{"type":"image_url","image_url":"data:image/png;base64,`+pngB64+`"}]}]}`)
	assertNoImageURL(t, msgs)
	assertBase64Image(t, contentBlocks(t, msgs[0])[0])
}

// The shape the pi coding agent produces for a tool that returns an image: a
// text-only tool result followed by a user turn carrying the image. The user
// turn is merged into the tool_result turn, so the image is its 3rd block.
func TestEnsureClaudeMessages_ToolResultImageTurn(t *testing.T) {
	msgs := claudeMessagesOf(t, `{"model":"claude-sonnet-5-5","messages":[
		{"role":"user","content":"screenshot it"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"shot","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"(see attached image)"},
		{"role":"user","content":[{"type":"text","text":"Attached image(s) from tool result:"},
			{"type":"image_url","image_url":{"url":"data:image/png;base64,`+pngB64+`"}}]}]}`)
	assertNoImageURL(t, msgs)
	blocks := contentBlocks(t, msgs[2])
	if blocks[0].(map[string]any)["type"] != "tool_result" {
		t.Fatalf("tool_result must stay first: %#v", blocks[0])
	}
	assertBase64Image(t, blocks[2])
}

func TestEnsureClaudeMessages_ToolResultContentImage(t *testing.T) {
	msgs := claudeMessagesOf(t, `{"model":"claude-sonnet-5-5","messages":[
		{"role":"user","content":"go"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"shot","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":[{"type":"text","text":"shot"},
			{"type":"image_url","image_url":{"url":"data:image/png;base64,`+pngB64+`"}}]}]}`)
	assertNoImageURL(t, msgs)
	tr := contentBlocks(t, msgs[2])[0].(map[string]any)
	if tr["type"] != "tool_result" || tr["tool_use_id"] != "c1" {
		t.Fatalf("tool_result lost: %#v", tr)
	}
	inner := tr["content"].([]any)
	if inner[0].(map[string]any)["type"] != "text" {
		t.Fatalf("tool_result text lost: %#v", inner)
	}
	assertBase64Image(t, inner[1])
}

func TestEnsureClaudeMessages_AssistantToolCallsKeepsImages(t *testing.T) {
	msgs := claudeMessagesOf(t, `{"model":"claude-sonnet-5-5","messages":[
		{"role":"user","content":"go"},
		{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,`+pngB64+`"}}],
			"tool_calls":[{"id":"c1","type":"function","function":{"name":"x","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"ok"}]}`)
	assertNoImageURL(t, msgs)
	assertBase64Image(t, contentBlocks(t, msgs[1])[0])
}

// A part Claude cannot represent is dropped, not forwarded: forwarding it would
// reproduce the 400 on every later turn of the session.
func TestEnsureClaudeMessages_UnrepresentableImageDropped(t *testing.T) {
	msgs := claudeMessagesOf(t, `{"model":"claude-sonnet-5-5","messages":[
		{"role":"user","content":[{"type":"text","text":"hi"},
			{"type":"image_url","image_url":{"url":"file:///etc/passwd"}},
			{"type":"image_url","image_url":{"url":"data:image/png,notbase64"}}]}]}`)
	assertNoImageURL(t, msgs)
	blocks := contentBlocks(t, msgs[0])
	if len(blocks) != 1 || blocks[0].(map[string]any)["type"] != "text" {
		t.Fatalf("only the text part must remain: %#v", blocks)
	}
}

func TestConvertClaudeImageParts_NoImageIsUntouched(t *testing.T) {
	in := []any{map[string]any{"type": "text", "text": "hi"}}
	out := convertClaudeImageParts(in).([]any)
	if len(out) != 1 || &out[0] != &in[0] {
		t.Fatalf("a content array without images must be returned as-is")
	}
	if convertClaudeImageParts("plain") != "plain" {
		t.Fatalf("string content must be returned as-is")
	}
}

func TestConvertClaudeImageParts_DoesNotMutateInput(t *testing.T) {
	in := []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + pngB64}}}
	_ = convertClaudeImageParts(in)
	if in[0].(map[string]any)["type"] != "image_url" {
		t.Fatalf("input was mutated: %#v", in)
	}
}

const unrepresentableImg = `{"type":"image_url","image_url":{"url":"file:///tmp/a.png"}}`

// A user turn whose only part is an image Claude cannot represent must be
// dropped, not forwarded as `content: []`, which Anthropic rejects and which
// then poisons every later turn of the conversation.
func TestEnsureClaudeMessages_AllDroppedUserTurnIsRemoved(t *testing.T) {
	msgs := claudeMessagesOf(t, `{"model":"claude-sonnet-5-5","messages":[
		{"role":"user","content":"hello"},
		{"role":"assistant","content":"ok"},
		{"role":"user","content":[`+unrepresentableImg+`]},
		{"role":"assistant","content":"saw it"},
		{"role":"user","content":"again"}]}`)
	for i, m := range msgs {
		if c, ok := m.(map[string]any)["content"].([]any); ok && len(c) == 0 {
			t.Fatalf("message %d still has empty content: %#v", i, msgs)
		}
		if _, leaked := m.(map[string]any)[imageEmptiedMarker]; leaked {
			t.Fatalf("internal marker leaked into message %d: %#v", i, m)
		}
	}
	// user, assistant, (dropped), assistant, user -> the two assistant turns merge.
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages after the drop and same-role merge, got %d: %#v", len(msgs), msgs)
	}
	roles := []string{"user", "assistant", "user"}
	for i, want := range roles {
		if got := msgs[i].(map[string]any)["role"]; got != want {
			t.Fatalf("message %d role = %v, want %v: %#v", i, got, want, msgs)
		}
	}
}

// Dropping the only turn would leave an empty `messages` array, which is its own
// rejection, so the turns are kept and the caller gets the provider's reason.
func TestEnsureClaudeMessages_AllDroppedOnlyTurnIsKept(t *testing.T) {
	msgs := claudeMessagesOf(t, `{"model":"claude-sonnet-5-5","messages":[
		{"role":"user","content":[`+unrepresentableImg+`]}]}`)
	if len(msgs) != 1 {
		t.Fatalf("an emptied-only conversation must keep its turn, got %#v", msgs)
	}
	if _, leaked := msgs[0].(map[string]any)[imageEmptiedMarker]; leaked {
		t.Fatalf("internal marker leaked: %#v", msgs[0])
	}
}

// A turn that still has text after the image is dropped is not touched.
func TestEnsureClaudeMessages_PartiallyDroppedTurnSurvives(t *testing.T) {
	msgs := claudeMessagesOf(t, `{"model":"claude-sonnet-5-5","messages":[
		{"role":"user","content":[{"type":"text","text":"look"},`+unrepresentableImg+`]}]}`)
	if len(msgs) != 1 || len(contentBlocks(t, msgs[0])) != 1 {
		t.Fatalf("the text part must survive: %#v", msgs)
	}
}

// A message the client itself sent with an empty array is not ours to repair.
func TestEmptiedByImageDrop_ClientEmptyArrayIsNotReported(t *testing.T) {
	if emptiedByImageDrop([]any{}, []any{}) {
		t.Fatalf("a client-sent empty array must not be reported as emptied by the drop")
	}
	if emptiedByImageDrop("plain", "plain") {
		t.Fatalf("string content must not be reported")
	}
	if !emptiedByImageDrop([]any{map[string]any{"type": "image_url"}}, []any{}) {
		t.Fatalf("an array reduced to nothing must be reported")
	}
}

// The media type is whatever precedes the first ';', and the remainder must be
// exactly "base64" (upstream: /^data:([^;]+);base64,([\s\S]+)$/). A parameter
// such as ";charset=utf-8" makes the part unrepresentable: forwarding it as
// media_type "image/png;charset=utf-8" is a 400 from Anthropic.
func TestClaudeImageBlock_DataURIMediaType(t *testing.T) {
	cases := []struct {
		url       string
		wantKeep  bool
		wantMedia string
	}{
		{"data:image/png;base64,AAAA", true, "image/png"},
		{"data:image/svg+xml;base64,AAAA", true, "image/svg+xml"},
		{"data:image/png;charset=utf-8;base64,AAAA", false, ""},
		{"data:image/png;name=a.png;base64,AAAA", false, ""},
		{"data:image/png;base64,", false, ""},
		{"data:;base64,AAAA", false, ""},
		{"data:image/png,AAAA", false, ""},
		{"data:image/png;BASE64,AAAA", false, ""},
	}
	for _, c := range cases {
		block, ok := claudeImageBlock(map[string]any{"image_url": map[string]any{"url": c.url}})
		if ok != c.wantKeep {
			t.Errorf("%q: kept=%v, want %v (%#v)", c.url, ok, c.wantKeep, block)
			continue
		}
		if !ok {
			continue
		}
		got := block["source"].(map[string]any)["media_type"]
		if got != c.wantMedia {
			t.Errorf("%q: media_type=%v, want %q", c.url, got, c.wantMedia)
		}
	}
}
