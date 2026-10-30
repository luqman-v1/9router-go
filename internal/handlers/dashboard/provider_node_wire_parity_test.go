package dashboard

import (
	"slices"
	"testing"

	"9router/proxy/internal/providers"
)

// nodeIDPrefixes and providers.CompatibleNodeWires describe the same set of
// custom node types from two sides: the dashboard composes ids from the
// prefixes, the request path derives lane and wire format from the table.
// When they drift, a node type becomes creatable in the dashboard while the
// request path has no wire contract for it — exactly how the
// "no choices in OpenAI response" bug (PR #797) survived: the node type
// existed, its format declaration did not. This test makes adding one side
// without the other a build failure.
func TestNodeIDPrefixesCoverEveryWireContract(t *testing.T) {
	prefixes := slices.Clone(nodeIDPrefixes)
	slices.Sort(prefixes)

	var declared []string
	for nodeType := range providers.CompatibleNodeWires {
		declared = append(declared, nodeType)
	}
	slices.Sort(declared)

	if !slices.Equal(prefixes, declared) {
		t.Errorf("nodeIDPrefixes %v and providers.CompatibleNodeWires keys %v disagree — a custom node type must be declared in both, wire contract included", prefixes, declared)
	}
}
