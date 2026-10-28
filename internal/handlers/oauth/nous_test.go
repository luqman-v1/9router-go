package oauth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestNousDeviceCodeStart(t *testing.T) {
	var postedForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		postedForm, _ = url.ParseQuery(string(bodyBytes))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"device_code": "dev_123",
			"user_code": "NOUS-1234",
			"verification_uri": "https://portal.nousresearch.com/device",
			"verification_uri_complete": "https://portal.nousresearch.com/device?user_code=NOUS-1234",
			"expires_in": 900,
			"interval": 5
		}`))
	}))
	defer server.Close()

	origURL := nousDeviceCodeURL
	nousDeviceCodeURL = server.URL
	defer func() { nousDeviceCodeURL = origURL }()

	res, err := nousStart()
	if err != nil {
		t.Fatalf("nousStart failed: %v", err)
	}

	if postedForm.Get("client_id") != "hermes-cli" {
		t.Errorf("expected client_id hermes-cli, got %q", postedForm.Get("client_id"))
	}
	if postedForm.Get("scope") != "inference:invoke inference:mint_agent_key" {
		t.Errorf("expected scope 'inference:invoke inference:mint_agent_key', got %q", postedForm.Get("scope"))
	}

	if res["device_code"] != "dev_123" {
		t.Errorf("expected device_code dev_123, got %v", res["device_code"])
	}
	if res["user_code"] != "NOUS-1234" {
		t.Errorf("expected user_code NOUS-1234, got %v", res["user_code"])
	}
	if res["verification_uri"] != "https://portal.nousresearch.com/device" {
		t.Errorf("expected verification_uri https://portal.nousresearch.com/device, got %v", res["verification_uri"])
	}
}

func TestNousDeviceCodePoll(t *testing.T) {
	var postedTokenForm url.Values
	var agentKeyAuthHeader string
	var agentKeyBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			bodyBytes, _ := io.ReadAll(r.Body)
			postedTokenForm, _ = url.ParseQuery(string(bodyBytes))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"access_token": "at_raw_123",
				"refresh_token": "rt_raw_456",
				"expires_in": 3600
			}`))
		case "/agent-key":
			agentKeyAuthHeader = r.Header.Get("Authorization")
			bodyBytes, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(bodyBytes, &agentKeyBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"api_key": "sk-nous-agent-minted",
				"key_id": "key_abc",
				"expires_at": "2026-10-15T00:00:00Z"
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	origTokenURL := nousTokenURL
	origAgentKeyURL := nousAgentKeyURL
	nousTokenURL = server.URL + "/token"
	nousAgentKeyURL = server.URL + "/agent-key"
	defer func() {
		nousTokenURL = origTokenURL
		nousAgentKeyURL = origAgentKeyURL
	}()

	tokens, err := nousPoll("dev_123")
	if err != nil {
		t.Fatalf("nousPoll failed: %v", err)
	}

	if postedTokenForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
		t.Errorf("expected grant_type urn:ietf:params:oauth:grant-type:device_code, got %q", postedTokenForm.Get("grant_type"))
	}
	if postedTokenForm.Get("client_id") != "hermes-cli" {
		t.Errorf("expected client_id hermes-cli, got %q", postedTokenForm.Get("client_id"))
	}
	if postedTokenForm.Get("device_code") != "dev_123" {
		t.Errorf("expected device_code dev_123, got %q", postedTokenForm.Get("device_code"))
	}

	if agentKeyAuthHeader != "Bearer at_raw_123" {
		t.Errorf("expected agentKey Authorization 'Bearer at_raw_123', got %q", agentKeyAuthHeader)
	}
	if agentKeyBody["min_ttl_seconds"] != float64(7200) {
		t.Errorf("expected min_ttl_seconds 7200, got %v", agentKeyBody["min_ttl_seconds"])
	}

	if tokens.access != "at_raw_123" {
		t.Errorf("expected access at_raw_123, got %q", tokens.access)
	}
	if tokens.refresh != "rt_raw_456" {
		t.Errorf("expected refresh rt_raw_456, got %q", tokens.refresh)
	}
	if tokens.expiresIn != 3600 {
		t.Errorf("expected expiresIn 3600, got %d", tokens.expiresIn)
	}
	if tokens.extra == nil || tokens.extra["apiKey"] != "sk-nous-agent-minted" {
		t.Errorf("expected extra.apiKey sk-nous-agent-minted, got %v", tokens.extra)
	}
	if tokens.extra["agentKeyId"] != "key_abc" {
		t.Errorf("expected extra.agentKeyId key_abc, got %v", tokens.extra["agentKeyId"])
	}
}
