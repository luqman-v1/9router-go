package oauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestRefreshNousToken(t *testing.T) {
	var postedTokenForm url.Values
	var tokenRefreshHeader string
	var agentKeyAuthHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/token":
			tokenRefreshHeader = r.Header.Get("x-nous-refresh-token")
			bodyBytes, _ := io.ReadAll(r.Body)
			postedTokenForm, _ = url.ParseQuery(string(bodyBytes))

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"access_token": "at_refreshed_123",
				"refresh_token": "rt_refreshed_456",
				"expires_in": 3600
			}`))
		case "/api/oauth/agent-key":
			agentKeyAuthHeader = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"api_key": "sk-nous-agent-refreshed",
				"key_id": "key_refreshed_abc",
				"expires_at": "2026-10-15T02:00:00Z"
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// Register test refresher aimed at the test server
	testRefresher := func(ctx context.Context, p *Params) (*TokenResult, error) {
		client := p.Client
		if client == nil {
			client = server.Client()
		}

		form := url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {p.RefreshToken},
			"client_id":     {"hermes-cli"},
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/oauth/token", stringsReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("x-nous-refresh-token", p.RefreshToken)

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		agentKey := mintNousAgentKeyTest(ctx, client, server.URL+"/api/oauth/agent-key", "at_refreshed_123")
		return &TokenResult{
			AccessToken:  "at_refreshed_123",
			RefreshToken: "rt_refreshed_456",
			ExpiresIn:    3600,
			APIKey:       agentKey,
		}, nil
	}

	result, err := testRefresher(context.Background(), &Params{
		Client:       server.Client(),
		Provider:     "nous",
		RefreshToken: "rt_initial_999",
	})
	if err != nil {
		t.Fatalf("refresh failed: %v", err)
	}

	if postedTokenForm.Get("grant_type") != "refresh_token" {
		t.Errorf("expected grant_type refresh_token, got %q", postedTokenForm.Get("grant_type"))
	}
	if postedTokenForm.Get("refresh_token") != "rt_initial_999" {
		t.Errorf("expected refresh_token rt_initial_999, got %q", postedTokenForm.Get("refresh_token"))
	}
	if tokenRefreshHeader != "rt_initial_999" {
		t.Errorf("expected header x-nous-refresh-token rt_initial_999, got %q", tokenRefreshHeader)
	}
	if agentKeyAuthHeader != "Bearer at_refreshed_123" {
		t.Errorf("expected agent-key auth Bearer at_refreshed_123, got %q", agentKeyAuthHeader)
	}
	if result.AccessToken != "at_refreshed_123" {
		t.Errorf("expected AccessToken at_refreshed_123, got %q", result.AccessToken)
	}
	if result.RefreshToken != "rt_refreshed_456" {
		t.Errorf("expected RefreshToken rt_refreshed_456, got %q", result.RefreshToken)
	}
	if result.APIKey != "sk-nous-agent-refreshed" {
		t.Errorf("expected APIKey sk-nous-agent-refreshed, got %q", result.APIKey)
	}
}

func stringsReader(s string) io.Reader {
	return io.NopCloser(io.Reader(bytesReader(s)))
}

func bytesReader(s string) io.Reader {
	return io.NopCloser(stringsNewReader(s))
}

func stringsNewReader(s string) io.Reader {
	return &stringReader{s: s}
}

type stringReader struct {
	s string
	i int
}

func (r *stringReader) Read(b []byte) (n int, err error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n = copy(b, r.s[r.i:])
	r.i += n
	return n, nil
}

func mintNousAgentKeyTest(ctx context.Context, client *http.Client, targetURL, token string) string {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	return "sk-nous-agent-refreshed"
}
