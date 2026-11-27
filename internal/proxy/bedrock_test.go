package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// TestBedrockOverrideIsSignedWithRequest is the Bedrock half of
// TestHeaderOverridesReachEveryForwarder.
//
// Bedrock is the failure-prone lane: AWS rejects a header it did not
// authenticate, so an override applied after aws.SignRequest would look correct
// on the wire and fail in production with SignatureDoesNotMatch. This test
// asserts two things about the request that actually reaches the endpoint:
//
//  1. the operator's header arrived, and
//  2. the SigV4 SignedHeaders list names it, which is only true when the
//     override joined the map before signing.
//
// A fake endpoint is dialled through cfg.BaseURL, which bedrockRuntimeURL
// honours so a VPC endpoint or a local fake can stand in for the public host.
func TestBedrockOverrideIsSignedWithRequest(t *testing.T) {
	type seen struct {
		value string
		authz string
	}
	got := make(chan seen, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case got <- seen{
			value: r.Header.Get("X-Operator-Trace"),
			authz: r.Header.Get("Authorization"),
		}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{
		BaseURL:         srv.URL,
		Format:          "claude",
		HeaderOverrides: map[string]string{"X-Operator-Trace": "abc123"},
	}
	psd := map[string]any{
		"region":      "us-east-1",
		"accessKeyId": "AKIAIOSFODNN7EXAMPLE",
	}
	body := []byte(`{"model":"us.anthropic.claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`)

	resp, err := ForwardBedrock(t.Context(), srv.Client(), cfg, "bedrock", "secret-key-for-test", body, false, psd)
	if err != nil {
		t.Fatalf("ForwardBedrock: %v", err)
	}
	resp.Body.Close()

	select {
	case s := <-got:
		if s.value != "abc123" {
			t.Errorf("X-Operator-Trace = %q, want %q", s.value, "abc123")
		}
		// SignedHeaders is the semicolon-joined lowercase name list inside the
		// credential scope line. Finding the override there proves it was part
		// of the canonical request, not appended after signing.
		if !strings.Contains(strings.ToLower(s.authz), "x-operator-trace") {
			t.Errorf("Authorization SignedHeaders does not cover x-operator-trace: %q", s.authz)
		}
	default:
		t.Fatal("bedrock endpoint received no request")
	}
}
