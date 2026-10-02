package translator

import (
	json "encoding/json/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A tool loop's request ends with the results of the last assistant turn's
// tool calls. Those sit AFTER that turn's breakpoint, so without a fourth one
// they are billed at the full input price and only written to the cache by the
// *next* request, which appends to them. Upstream #49c761cd gives them their
// own 5m breakpoint while the marker budget has room.
//
// The cache anchor that exists here re-pins the head (last system block, last
// cacheable tool) and trims whatever the client spent; these tests pin the one
// thing that pass adds on top, and that the trimming still holds.
const toolLoopBody = `{
	"model": "claude-sonnet-4-5",
	"system": [{"type": "text", "text": "You are an agent."}],
	"tools": [
		{"name": "read_file", "description": "d", "input_schema": {"type": "object", "properties": {}}},
		{"name": "run_command", "description": "d", "input_schema": {"type": "object", "properties": {}}}
	],
	"messages": [
		{"role": "user", "content": [{"type": "text", "text": "Fix the bug."}]},
		{"role": "assistant", "content": [
			{"type": "text", "text": "Reading."},
			{"type": "tool_use", "id": "t1", "name": "read_file", "input": {"path": "a"}}
		]},
		{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "file"}]},
		{"role": "assistant", "content": [
			{"type": "text", "text": "Patching."},
			{"type": "tool_use", "id": "t2", "name": "run_command", "input": {"cmd": "test"}}
		]},
		{"role": "user", "content": [
			{"type": "tool_result", "tool_use_id": "t2", "content": "a"},
			{"type": "tool_result", "tool_use_id": "t2b", "content": "b"}
		]}
	]
}`

func TestAnchorClaudeCache_FinalToolResultsTakeAFreeSlot(t *testing.T) {
	req := anchoredRequest(t, []byte(toolLoopBody))

	// head anchors (system, last tool) + the new breakpoint on the last block
	// of the final tool_result turn.
	assertMarkers(t, markerPositions(req), []string{"system[0]", "tools[1]", "messages[4].1"})

	cc, ok := blockAt(t, req, 4, 1)["cache_control"].(map[string]any)
	if !ok || cc["type"] != "ephemeral" || cc["ttl"] != nil {
		t.Errorf("final tool_result marker = %v, want a plain 5m ephemeral breakpoint", cc)
	}
}

// A tool loop with no tools array still gets its final results cached: the
// missing head anchor is exactly the room the extra breakpoint needs.
func TestAnchorClaudeCache_FinalToolResultsWithoutTools(t *testing.T) {
	req := anchoredRequest(t, []byte(`{
		"model": "claude-sonnet-4-5",
		"system": [{"type": "text", "text": "You are an agent."}],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Fix the bug."}]},
			{"role": "assistant", "content": [
				{"type": "text", "text": "Reading."},
				{"type": "tool_use", "id": "t1", "name": "read_file", "input": {"path": "a"}}
			]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "file"}]}
		]
	}`))

	assertMarkers(t, markerPositions(req), []string{"system[0]", "messages[2].0"})
}

// The new breakpoint must never push the request past what the Messages API
// accepts, however much of the budget the client already spent.
func TestAnchorClaudeCache_FinalToolResultsRespectTheBudget(t *testing.T) {
	tests := []struct {
		name string
		// clientMarkers are breakpoints already on the wire, as [message, block].
		// They ride on a tool loop with no system prompt and no tools, so the
		// only anchors are the ones the pass has to place itself.
		clientMarkers [][2]int
		wantTail      int
	}{
		{
			name:          "the last free slot goes to the final results",
			clientMarkers: [][2]int{{0, 0}, {1, 0}},
			wantTail:      1,
		},
		{
			name:          "a budget already spent on earlier turns is left alone",
			clientMarkers: [][2]int{{0, 0}, {1, 0}, {2, 0}, {3, 0}},
			wantTail:      0,
		},
		{
			name:          "a client that already marked the tail keeps its own breakpoint",
			clientMarkers: [][2]int{{4, 1}},
			wantTail:      1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := toolLoopWithClientMarkers(t, toolLoopWithoutHeadAnchors, tt.clientMarkers)

			req := anchoredRequest(t, body)

			if n := countCacheControlBlocks(req); n > cacheControlMarkerBudget {
				t.Fatalf("%d markers, the Messages API accepts at most %d: %v", n, cacheControlMarkerBudget, markerPositions(req))
			}
			if got := len(markersIn(req, 4)); got != tt.wantTail {
				t.Errorf("final tool_result turn has %d markers, want %d (%v)", got, tt.wantTail, markerPositions(req))
			}
		})
	}
}

// toolLoopWithoutHeadAnchors is the same tool loop stripped of its system
// prompt and tools array, so nothing but the client spends the budget.
const toolLoopWithoutHeadAnchors = `{
	"model": "claude-sonnet-4-5",
	"messages": [
		{"role": "user", "content": [{"type": "text", "text": "Fix the bug."}]},
		{"role": "assistant", "content": [
			{"type": "text", "text": "Reading."},
			{"type": "tool_use", "id": "t1", "name": "read_file", "input": {"path": "a"}}
		]},
		{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "file"}]},
		{"role": "assistant", "content": [
			{"type": "text", "text": "Patching."},
			{"type": "tool_use", "id": "t2", "name": "run_command", "input": {"cmd": "test"}}
		]},
		{"role": "user", "content": [
			{"type": "tool_result", "tool_use_id": "t2", "content": "a"},
			{"type": "tool_result", "tool_use_id": "t2b", "content": "b"}
		]}
	]
}`

// A request that ends on a typed user message is not a tool loop: nothing new
// arrived since the last assistant turn, so the tail earns no extra breakpoint.
func TestAnchorClaudeCache_TypedFinalTurnIsUntouched(t *testing.T) {
	body := withExtraTurns(toolLoopBody,
		`{"role": "assistant", "content": [{"type": "text", "text": "Done."}]},
		 {"role": "user", "content": [{"type": "text", "text": "Thanks, and the tests?"}]}`)

	req := anchoredRequest(t, []byte(body))

	// The same body with a tool_result tail spends a third marker (see
	// TestAnchorClaudeCache_FinalToolResultsTakeAFreeSlot); a typed tail
	// spends none.
	assertMarkers(t, markerPositions(req), []string{"system[0]", "tools[1]"})
}

// Re-anchoring an already anchored body must land on the same markers, or every
// step of a tool loop would invalidate the cache the previous one wrote.
func TestAnchorClaudeCache_FinalToolResultsAreIdempotent(t *testing.T) {
	first := markerPositions(anchoredRequest(t, []byte(toolLoopBody)))
	second := markerPositions(anchoredRequest(t, AnchorClaudeCache([]byte(toolLoopBody))))

	assertMarkers(t, second, first)
}

// toolLoopWithClientMarkers returns base with a client breakpoint planted on
// the given [message, block] positions.
func toolLoopWithClientMarkers(t *testing.T, base string, positions [][2]int) []byte {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal([]byte(base), &req); err != nil {
		t.Fatalf("unmarshal %s: %v", base, err)
	}
	msgs, _ := req["messages"].([]any)
	for _, pos := range positions {
		msg, ok := msgs[pos[0]].(map[string]any)
		if !ok {
			t.Fatalf("messages[%d] is %T, want an object", pos[0], msgs[pos[0]])
		}
		blocks, ok := msg["content"].([]any)
		if !ok || pos[1] >= len(blocks) {
			t.Fatalf("messages[%d].content[%d] out of range", pos[0], pos[1])
		}
		block, ok := blocks[pos[1]].(map[string]any)
		if !ok {
			t.Fatalf("messages[%d].content[%d] is %T, want an object", pos[0], pos[1], blocks[pos[1]])
		}
		block["cache_control"] = map[string]any{"type": "ephemeral"}
	}
	out, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return out
}

// withExtraTurns appends turns to a request body's messages array, so a test
// can vary the tail of the same conversation.
func withExtraTurns(body, extraTurns string) string {
	// Splice inside the messages array, not after the request object: the
	// string suffix is `]` then `}`, and the new turns belong between them.
	return strings.TrimSuffix(body, "]\n}") + ",\n\t\t" + extraTurns + "\n\t]\n}"
}

// anchoredRequest runs AnchorClaudeCache and decodes the result.
func anchoredRequest(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal(AnchorClaudeCache(body), &req); err != nil {
		t.Fatalf("unmarshal anchored body: %v", err)
	}
	return req
}

// markerPositions names every cache_control breakpoint in document order
// ("system[0]", "tools[1]", "messages[3].1") so a test can assert the exact
// anchoring rather than only counting markers.
func markerPositions(req map[string]any) []string {
	var out []string
	appendMarked := func(prefix string, items []any) {
		for i, item := range items {
			if m, ok := item.(map[string]any); ok && m["cache_control"] != nil {
				out = append(out, prefix+"["+strconv.Itoa(i)+"]")
			}
		}
	}
	if sys, ok := req["system"].([]any); ok {
		appendMarked("system", sys)
	}
	if tools, ok := req["tools"].([]any); ok {
		appendMarked("tools", tools)
	}
	for i, mRaw := range asMessages(req) {
		m, _ := mRaw.(map[string]any)
		blocks, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for j, bRaw := range blocks {
			if bm, ok := bRaw.(map[string]any); ok && bm["cache_control"] != nil {
				out = append(out, "messages["+strconv.Itoa(i)+"]."+strconv.Itoa(j))
			}
		}
	}
	return out
}

func asMessages(req map[string]any) []any {
	msgs, _ := req["messages"].([]any)
	return msgs
}

// markersIn lists the content-block indices carrying a breakpoint in one turn.
func markersIn(req map[string]any, msgIdx int) []int {
	var out []int
	msgs := asMessages(req)
	if msgIdx >= len(msgs) {
		return out
	}
	m, _ := msgs[msgIdx].(map[string]any)
	blocks, _ := m["content"].([]any)
	for j, bRaw := range blocks {
		if bm, ok := bRaw.(map[string]any); ok && bm["cache_control"] != nil {
			out = append(out, j)
		}
	}
	return out
}

func blockAt(t *testing.T, req map[string]any, msgIdx, blockIdx int) map[string]any {
	t.Helper()
	msgs := asMessages(req)
	if msgIdx >= len(msgs) {
		t.Fatalf("messages[%d] out of range in %v", msgIdx, markerPositions(req))
	}
	m, ok := msgs[msgIdx].(map[string]any)
	if !ok {
		t.Fatalf("messages[%d] is %T, want an object", msgIdx, msgs[msgIdx])
	}
	blocks, ok := m["content"].([]any)
	if !ok || blockIdx >= len(blocks) {
		t.Fatalf("messages[%d].content[%d] out of range in %v", msgIdx, blockIdx, markerPositions(req))
	}
	block, ok := blocks[blockIdx].(map[string]any)
	if !ok {
		t.Fatalf("messages[%d].content[%d] is %T, want an object", msgIdx, blockIdx, blocks[blockIdx])
	}
	return block
}

func assertMarkers(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("markers = %v, want %v", got, want)
	}
}
