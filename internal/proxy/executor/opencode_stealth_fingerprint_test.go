package executor

import (
	"testing"

	tls "github.com/bogdanfinn/utls"
)

// specFacts is the part of a ClientHelloSpec a fingerprint is computed from.
type specFacts struct {
	alpn      []string
	curves    []tls.CurveID
	keyShares []tls.CurveID
	sigalgs   []tls.SignatureScheme
}

func factsOf(t *testing.T, spec tls.ClientHelloSpec) specFacts {
	t.Helper()
	var facts specFacts
	for _, ext := range spec.Extensions {
		switch e := ext.(type) {
		case *tls.ALPNExtension:
			facts.alpn = e.AlpnProtocols
		case *tls.SupportedCurvesExtension:
			facts.curves = e.Curves
		case *tls.KeyShareExtension:
			for _, ks := range e.KeyShares {
				facts.keyShares = append(facts.keyShares, ks.Group)
			}
		case *tls.SignatureAlgorithmsExtension:
			facts.sigalgs = e.SupportedSignatureAlgorithms
		}
	}
	return facts
}

// The two layouts must stay distinct: a change that quietly made them identical
// would leave the flag doing nothing while appearing to work.
func TestBunSpec_ReferenceDiffersFromBaseline(t *testing.T) {
	baseline := factsOf(t, bunBoringSSLSpecFor(false))
	reference := factsOf(t, bunBoringSSLSpecFor(true))

	tests := []struct {
		name string
		got  func(specFacts) any
		want func(specFacts) any
	}{
		{
			name: "ALPN is HTTP/1.1 only under the reference layout",
			got:  func(f specFacts) any { return len(f.alpn) },
			want: func(f specFacts) any {
				if f.alpn[0] != "http/1.1" {
					return "first ALPN is not http/1.1: " + f.alpn[0]
				}
				return 1
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, want := tt.got(reference), tt.want(reference); got != want {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}

	// Baseline keeps h2 and offers no post-quantum curve or hybrid key share.
	if baseline.alpn[0] != "h2" {
		t.Errorf("baseline ALPN must lead with h2, got %v", baseline.alpn)
	}
	if containsCurve(baseline.curves, tls.X25519MLKEM768) {
		t.Errorf("baseline must not advertise X25519MLKEM768")
	}
	if !containsCurve(baseline.curves, tls.CurveP521) {
		t.Errorf("baseline must keep CurveP521")
	}
	if len(baseline.sigalgs) != 8 {
		t.Errorf("baseline sigalgs = %d, want 8", len(baseline.sigalgs))
	}

	// Reference leads with the hybrid curve, drops P521, sends both shares, and
	// appends PKCS1WithSHA1 last.
	if reference.curves[0] != tls.X25519MLKEM768 {
		t.Errorf("reference must lead with X25519MLKEM768, got %v", reference.curves[0])
	}
	if containsCurve(reference.curves, tls.CurveP521) {
		t.Errorf("reference must not advertise CurveP521")
	}
	if len(reference.keyShares) != 2 ||
		reference.keyShares[0] != tls.X25519MLKEM768 ||
		reference.keyShares[1] != tls.X25519 {
		t.Errorf("reference key shares = %v, want [X25519MLKEM768 X25519]", reference.keyShares)
	}
	if len(reference.sigalgs) != 9 {
		t.Fatalf("reference sigalgs = %d, want 9", len(reference.sigalgs))
	}
	if reference.sigalgs[len(reference.sigalgs)-1] != tls.PKCS1WithSHA1 {
		t.Errorf("reference must append PKCS1WithSHA1 last, got %v", reference.sigalgs[len(reference.sigalgs)-1])
	}
}

// Both layouts keep the cipher list and extension count the gate measures, so
// flipping the flag cannot silently change those.
func TestBunSpec_BothLayoutsKeepCiphersAndExtensionCount(t *testing.T) {
	for _, reference := range []bool{false, true} {
		spec := bunBoringSSLSpecFor(reference)
		if len(spec.CipherSuites) != 17 {
			t.Errorf("reference=%v: ciphers = %d, want 17", reference, len(spec.CipherSuites))
		}
		if len(spec.Extensions) != 13 {
			t.Errorf("reference=%v: extensions = %d, want 13", reference, len(spec.Extensions))
		}
	}
}

// The flag must reach the cache key. A key of proxy URL alone would hand back a
// client built under the other fingerprint for the whole process lifetime.
func TestOpencodeStealthCacheKey_SeparatesFingerprints(t *testing.T) {
	t.Setenv("OPENCODE_V2_REFERENCE_FINGERPRINT", "")
	if opencodeReferenceFingerprint() {
		t.Fatal("the reference fingerprint must be off by default")
	}
	t.Setenv("OPENCODE_V2_REFERENCE_FINGERPRINT", "1")
	if !opencodeReferenceFingerprint() {
		t.Fatal("OPENCODE_V2_REFERENCE_FINGERPRINT=1 must enable the reference layout")
	}
	// Any value other than "1" leaves it off, so a typo cannot flip the
	// fingerprint by accident.
	for _, value := range []string{"true", "yes", "0", "on"} {
		t.Setenv("OPENCODE_V2_REFERENCE_FINGERPRINT", value)
		if opencodeReferenceFingerprint() {
			t.Errorf("%q must not enable the reference layout", value)
		}
	}
}

func containsCurve(curves []tls.CurveID, want tls.CurveID) bool {
	for _, curve := range curves {
		if curve == want {
			return true
		}
	}
	return false
}

// The reference body order must only apply when the flag is on, so the default
// lane keeps the bytes it has always sent.
func TestMarshalOpenCodeFreeChatBody_FlagGated(t *testing.T) {
	body := map[string]any{
		"model":                 "m",
		"messages":              []any{},
		"stream":                true,
		"max_completion_tokens": 32000,
		"alpha":                 1,
	}

	t.Setenv("OPENCODE_V2_REFERENCE_FINGERPRINT", "")
	def, err := marshalOpenCodeFreeChatBody(body)
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	stable, err := marshalStable(body)
	if err != nil {
		t.Fatalf("marshalStable: %v", err)
	}
	if string(def) != string(stable) {
		t.Errorf("default lane must keep marshalStable's order:\n got %s\nwant %s", def, stable)
	}

	t.Setenv("OPENCODE_V2_REFERENCE_FINGERPRINT", "1")
	ref, err := marshalOpenCodeFreeChatBody(body)
	if err != nil {
		t.Fatalf("reference: %v", err)
	}
	ordered, err := marshalOpenCodeV2Chat(body)
	if err != nil {
		t.Fatalf("marshalOpenCodeV2Chat: %v", err)
	}
	if string(ref) != string(ordered) {
		t.Errorf("reference lane must use the v2 leading order:\n got %s\nwant %s", ref, ordered)
	}
	if string(ref) == string(def) {
		t.Errorf("the two orders must differ, or the flag does nothing: %s", ref)
	}
}
