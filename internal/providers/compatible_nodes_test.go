package providers

import "testing"

// The wire table is the contract every node-type reader derives from, so the
// table itself has to stay coherent: a lane is a path, and the pairing that
// the "no choices in OpenAI response" bug violated — a node whose lane is the
// Messages endpoint must be declared Claude, and a Claude declaration must
// dial the Messages endpoint — holds for every entry.
func TestCompatibleNodeWiresStayCoherent(t *testing.T) {
	if len(CompatibleNodeWires) == 0 {
		t.Fatal("CompatibleNodeWires is empty — the wire contract table was lost")
	}
	for nodeType, wire := range CompatibleNodeWires {
		if wire.Lane != "" && !startsWithSlash(wire.Lane) {
			t.Errorf("%s: lane %q is not a path (want a leading / or empty)", nodeType, wire.Lane)
		}
		isMessagesLane := wire.Lane == "/messages"
		isClaudeFormat := wire.Format == FormatClaude
		if isMessagesLane != isClaudeFormat {
			t.Errorf("%s: lane %q and format %q disagree — a node dialling /messages speaks Claude and vice versa; declare them together", nodeType, wire.Lane, wire.Format)
		}
	}
}

func startsWithSlash(s string) bool {
	return len(s) > 0 && s[0] == '/'
}
