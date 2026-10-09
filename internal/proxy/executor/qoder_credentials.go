package executor

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"time"
)

// Qoder credential helpers — port of open-sse/shared/qoder/cosy.js
// (isQoderPat, resolveQoderCredentials, exchangeJobToken) and the userinfo
// lookup upstream uses to recover a job token's user id.
//
// A Personal Access Token (pt-…) is stored on the connection like any other
// credential but cannot sign COSY requests, so both the dashboard's model list
// and chat have to exchange it first. The exchange is a plain JSON POST, not
// COSY-signed.

// QoderDoer performs one outbound request and returns its status plus a
// truncated body.
//
// It exists so the dashboard can route these calls through its own probe seam
// (which honours a connection's proxy-bound client and is stubbed in tests)
// while chat routes them through the proxied transport. One implementation of
// the exchange, two transports.
type QoderDoer func(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error)

// qoderCredentialTimeout bounds one credential exchange when no doer supplies
// its own deadline.
const qoderCredentialTimeout = 20 * time.Second

// qoderProbeUserAgent is what the official CLI announces. Qoder's WAF answers a
// default Go User-Agent with a bare 403.
const qoderProbeUserAgent = "qodercli/1.0.0"

// qoderUserInfoResponse is the subset of /api/v1/userinfo that matters here.
// Qoder has shipped more than one spelling of the id field.
type qoderUserInfoResponse struct {
	ID        string `json:"id"`
	UserID    string `json:"userId"`
	UserIDAlt string `json:"user_id"`
}

// ExchangeQoderJobToken trades a Personal Access Token for a short-lived job
// token (jt-…).
func ExchangeQoderJobToken(ctx context.Context, do QoderDoer, exchangeURL, pat string) (string, error) {
	payload, err := json.Marshal(map[string]string{"personal_token": pat})
	if err != nil {
		return "", fmt.Errorf("qoder PAT exchange: %w", err)
	}
	status, body, err := do(ctx, "POST", exchangeURL, map[string]string{
		"Content-Type":    "application/json",
		"Accept":          "application/json",
		"User-Agent":      qoderProbeUserAgent,
		"Cosy-Version":    "1.0.0",
		"Cosy-ClientType": "5",
	}, payload)
	if err != nil {
		return "", fmt.Errorf("qoder PAT exchange failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("qoder PAT exchange failed: %d %s", status, qoderTruncate(string(body), 200))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("qoder PAT exchange returned non-JSON")
	}
	if strings.TrimSpace(out.Token) == "" {
		return "", fmt.Errorf("qoder PAT exchange returned no job token")
	}
	return out.Token, nil
}

// FetchQoderUserID resolves the userId a job token belongs to. Best-effort, as
// upstream: it returns "" on any failure and the caller falls back to the id
// already stored on the connection.
func FetchQoderUserID(ctx context.Context, do QoderDoer, userinfoURL, jobToken string) string {
	status, body, err := do(ctx, "GET", userinfoURL, map[string]string{
		"Authorization": "Bearer " + jobToken,
		"Accept":        "application/json",
		"User-Agent":    qoderProbeUserAgent,
	}, nil)
	if err != nil || status < 200 || status >= 300 {
		return ""
	}
	var out qoderUserInfoResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return ""
	}
	for _, candidate := range []string{out.ID, out.UserID, out.UserIDAlt} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
