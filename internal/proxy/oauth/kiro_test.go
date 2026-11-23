package oauth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRefreshKiroSocial_SendsIDEIdentityHeaders(t *testing.T) {
	var capturedUA, capturedAmzUA string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		capturedUA = r.Header.Get("User-Agent")
		capturedAmzUA = r.Header.Get("x-amz-user-agent")
		body := `{"accessToken":"new-access","refreshToken":"new-refresh","expiresIn":3600}`
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}
		return resp, nil
	})
	client := &http.Client{Transport: rt}

	res, err := refreshKiroSocial(context.Background(), client, "old-refresh-token")
	if err != nil {
		t.Fatalf("refreshKiroSocial: %v", err)
	}
	if res.AccessToken != "new-access" {
		t.Errorf("AccessToken = %q, want new-access", res.AccessToken)
	}
	if !strings.Contains(capturedUA, "api/codewhispererruntime") {
		t.Errorf("User-Agent missing codewhispererruntime, got %q", capturedUA)
	}
	if !strings.Contains(capturedUA, "m/N,E") {
		t.Errorf("User-Agent missing m/N,E, got %q", capturedUA)
	}
	if !strings.Contains(capturedUA, "KiroIDE-1.4.154-") {
		t.Errorf("User-Agent missing KiroIDE-1.4.154-, got %q", capturedUA)
	}
	if !strings.Contains(capturedAmzUA, "KiroIDE-1.4.154-") {
		t.Errorf("x-amz-user-agent missing KiroIDE-1.4.154-, got %q", capturedAmzUA)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
