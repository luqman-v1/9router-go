package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// rewriteRoundTripper lets a test redirect the hardcoded Kiro refresh URL to
// an httptest server while observing the exact request the refresher builds.
type rewriteRoundTripper func(*http.Request) (*http.Response, error)

func (f rewriteRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func kiroTestMachineID(t *testing.T, seed string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// The social refresh must carry the full runtime identity on the wire — not
// the old hardcoded "kiro-cli/1.0.0" User-Agent, and not just one of the two
// headers. The headers are captured from the actual outgoing request.
func TestRefreshKiroSocialSendsRuntimeIdentityHeadersOnWire(t *testing.T) {
	const refreshToken = "social-refresh-token-for-onwire-test"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"accessToken":"refreshed-access-token","expiresIn":3600}`))
	}))
	defer srv.Close()

	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	base := srv.Client().Transport
	if base == nil {
		base = http.DefaultTransport
	}

	var captured http.Header
	client := &http.Client{Transport: rewriteRoundTripper(func(req *http.Request) (*http.Response, error) {
		const wantURL = "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken"
		if got := req.URL.String(); got != wantURL {
			t.Errorf("refresh request URL = %q, want %q", got, wantURL)
		}
		captured = req.Header.Clone()
		clone := req.Clone(req.Context())
		clone.URL.Scheme = target.Scheme
		clone.URL.Host = target.Host
		return base.RoundTrip(clone)
	})}

	res, err := refreshKiro(context.Background(), &Params{
		Client:       client,
		RefreshToken: refreshToken,
	})
	if err != nil {
		t.Fatalf("refreshKiro(social) failed: %v", err)
	}
	if res.AccessToken != "refreshed-access-token" || res.ExpiresIn != 3600 {
		t.Fatalf("unexpected refresh result: %+v", res)
	}
	if captured == nil {
		t.Fatal("no outgoing request was captured")
	}

	machineID := kiroTestMachineID(t, refreshToken)
	wantUA := "aws-sdk-js/1.0.0 ua/2.1 os/windows#10.0.26200 lang/js md/nodejs#22.21.1 " +
		"api/codewhispererruntime#1.0.0 m/N,E KiroIDE-1.4.154-" + machineID
	wantAmzUA := "aws-sdk-js/1.0.0 KiroIDE-1.4.154-" + machineID

	if got := captured.Get("User-Agent"); got != wantUA {
		t.Errorf("outgoing User-Agent = %q, want %q", got, wantUA)
	}
	if got := captured.Get("X-Amz-User-Agent"); got != wantAmzUA {
		t.Errorf("outgoing X-Amz-User-Agent = %q, want %q", got, wantAmzUA)
	}
	if strings.Contains(captured.Get("User-Agent"), "kiro-cli") {
		t.Errorf("outgoing User-Agent still uses the hardcoded kiro-cli identity: %q", captured.Get("User-Agent"))
	}
}
