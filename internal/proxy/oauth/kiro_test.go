package oauth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// captureTransport intercepts refresh requests without network and returns a
// canned token response. No real credential ever leaves the test process.
type captureTransport struct {
	ua    string
	amzUA string
	path  string
	calls int
}

func (tr *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr.calls++
	tr.ua = req.Header.Get("User-Agent")
	tr.amzUA = req.Header.Get("x-amz-user-agent")
	tr.path = req.URL.Path
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"accessToken":"new-access-token","refreshToken":"new-refresh-token","expiresIn":3600}`,
		)),
		Request: req,
	}, nil
}

func assertKiroIdentityHeaders(t *testing.T, tr *captureTransport) {
	t.Helper()
	if tr.calls != 1 {
		t.Fatalf("expected exactly one refresh request, got %d", tr.calls)
	}
	for name, value := range map[string]string{"User-Agent": tr.ua, "x-amz-user-agent": tr.amzUA} {
		if !strings.Contains(value, "KiroIDE-1.4.154-") {
			t.Errorf("%s missing KiroIDE identity: %q", name, value)
		}
		if strings.Contains(value, "kiro-cli/1.0.0") {
			t.Errorf("%s still carries legacy identity: %q", name, value)
		}
	}
}

func TestRefreshKiroAWSIdentityHeaders(t *testing.T) {
	tr := &captureTransport{}
	client := &http.Client{Transport: tr}
	got, err := refreshKiroAWS(context.Background(), client, "rt-secret", "cid-secret", "cs-secret", "us-east-1")
	if err != nil {
		t.Fatalf("refreshKiroAWS failed: %v", err)
	}
	if got.AccessToken != "new-access-token" {
		t.Errorf("access token = %q", got.AccessToken)
	}
	if tr.path != "/token" {
		t.Errorf("path = %q, want /token", tr.path)
	}
	assertKiroIdentityHeaders(t, tr)
}

func TestRefreshKiroSocialIdentityHeaders(t *testing.T) {
	tr := &captureTransport{}
	client := &http.Client{Transport: tr}
	got, err := refreshKiroSocial(context.Background(), client, "rt-secret")
	if err != nil {
		t.Fatalf("refreshKiroSocial failed: %v", err)
	}
	if got.AccessToken != "new-access-token" {
		t.Errorf("access token = %q", got.AccessToken)
	}
	assertKiroIdentityHeaders(t, tr)
}
