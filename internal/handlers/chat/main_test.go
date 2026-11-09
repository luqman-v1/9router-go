package chat

import (
	"os"
	"testing"

	"9router/proxy/internal/testnet"
)

// ALLOW_PRIVATE_NETWORKING lets the handler tests reach their own httptest
// servers; testnet.Seal keeps those servers the only reachable destination.
//
// Usage writes run inline in this suite: the tests read usage rows straight
// after the response, and the async writer's own tests opt out explicitly and
// flush.
func TestMain(m *testing.M) {
	os.Setenv("ALLOW_PRIVATE_NETWORKING", "true")
	testnet.Seal()
	SetUsageWritesSynchronousForTest(true)
	os.Exit(m.Run())
}
