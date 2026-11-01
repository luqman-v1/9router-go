package executor

import (
	"encoding/json"
	"strings"
	"testing"
)

// The identity asset is no longer decoded in init() (§4.E, Zero Panic), so a
// malformed zcode_identity_system.json would no longer crash the process — it
// would instead leave every plan-lane request with empty cliPrefix and
// stablePrefix and come back as an opaque 3012. This test is the gate that
// replaces the panic: it fails the build instead of a user's request.
func TestZcodeIdentityAssetParses(t *testing.T) {
	if err := zcodeIdentityError(); err != nil {
		t.Fatalf("embedded identity asset did not decode: %v", err)
	}

	asset := zcodeIdentityValues()
	tests := []struct {
		name string
		got  string
	}{
		// The gateway content-inspects both prefixes byte for byte. An empty
		// one is the failure mode a missing key would produce, and it is
		// rejected with 405 / code 3012 at request time.
		{"cliPrefix is present", asset.CliPrefix},
		{"stablePrefix is present", asset.StablePrefix},
		{"anthropicVersion is present", asset.AnthropicVersion},
		{"sdkUserAgent is present", asset.SDKUserAgent},
	}
	for _, tt := range tests {
		if strings.TrimSpace(tt.got) == "" {
			t.Errorf("%s: field decoded empty", tt.name)
		}
	}

	// The stable prefix starts with a newline by design: it is pasted after the
	// CLI prefix in the same block, and the gateway compares it byte-exact.
	if !strings.HasPrefix(asset.StablePrefix, "\n") {
		t.Errorf("stablePrefix lost its leading newline; the gateway compares it byte-exact")
	}
}

// TestZcodeIdentityDecodeIsCached pins the sync.OnceValue contract: the asset is
// decoded once, so a request path reading it twice pays one unmarshal, and the
// error stays stable between calls instead of depending on call order.
func TestZcodeIdentityDecodeIsCached(t *testing.T) {
	if err := zcodeIdentityError(); err != nil {
		t.Fatalf("first decode: %v", err)
	}
	if err := zcodeIdentityError(); err != nil {
		t.Fatalf("second decode reported an error after a successful first: %v", err)
	}
	if first, second := zcodeIdentityValues(), zcodeIdentityValues(); first != second {
		t.Errorf("zcodeIdentityValues() returned different values on successive calls")
	}
}

// TestZcodeSystemBlocksCarryAssetPrefixes guards the three-block shape: block 1
// carries cliPrefix verbatim, block 2 carries the canonical stablePrefix with
// cache_control, block 3 carries the client system. A shift in that order is
// what the gateway rejects, and it is invisible in a passing request otherwise.
func TestZcodeSystemBlocksCarryAssetPrefixes(t *testing.T) {
	asset := zcodeIdentityValues()
	blocks := zcodeSystemBlocks("client system text")
	if len(blocks) != 3 {
		t.Fatalf("system blocks = %d, want 3", len(blocks))
	}

	wantText := []string{asset.CliPrefix, asset.StablePrefix, "\n\nclient system text"}
	for i, want := range wantText {
		block, ok := blocks[i].(map[string]any)
		if !ok {
			t.Fatalf("block %d is not a map", i)
		}
		if block["text"] != want {
			t.Errorf("block %d text does not match the expected prefix", i)
		}
	}

	// Blocks 2 and 3 are the cacheable pair; block 1 is not marked.
	for _, i := range []int{1, 2} {
		block := blocks[i].(map[string]any)
		if _, ok := block["cache_control"]; !ok {
			t.Errorf("block %d carries no cache_control", i)
		}
	}
	if _, ok := blocks[0].(map[string]any)["cache_control"]; ok {
		t.Errorf("block 0 must not carry cache_control")
	}
}

// TestZcodeIdentityAssetIgnoresUnknownKeys pins that the decode does not insist
// on a closed schema: the capture carries a `source` note, and a future capture
// may add more. Tightening the struct must not turn that into a request error.
func TestZcodeIdentityAssetIgnoresUnknownKeys(t *testing.T) {
	var asset zcodeIdentityAsset
	raw := []byte(`{"cliPrefix":"a","stablePrefix":"\nb","newFieldFromAFutureCapture":"x"}`)
	if err := json.Unmarshal(raw, &asset); err != nil {
		t.Fatalf("decode rejected an unknown key: %v", err)
	}
	if asset.CliPrefix != "a" || asset.StablePrefix != "\nb" {
		t.Errorf("decode dropped known fields: %+v", asset)
	}
}
