package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// Upstream calls for the free-pi device flow, split out from the HTTP handlers
// so the request/response shapes live in one place and the handlers stay
// readable. Every shape here is pinned by a fixture in
// internal/providers/testdata/freepi.

// freePiClient is the HTTP client for auth and catalogue calls. It uses the
// proxy-aware transport for the same reason deviceClient does: a sandbox or
// corporate proxy would otherwise turn every call into an opaque transport
// error.
var freePiClient = &http.Client{
	Transport: proxy.NewFallbackTransport(http.DefaultTransport),
	Timeout:   20 * time.Second,
}

// freePiDeviceResponse is the POST /auth/github/device reply.
type freePiDeviceResponse struct {
	SessionID               string `json:"session_id"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	Interval                int    `json:"interval"`
	ExpiresIn               int    `json:"expires_in"`
}

// freePiPollStatus classifies one POST /auth/token reply.
type freePiPollStatus int

const (
	// freePiPollPending means GitHub has not authorised yet; poll again.
	freePiPollPending freePiPollStatus = iota
	// freePiPollGranted means a token was issued.
	freePiPollGranted
	// freePiPollConsentRequired means the server has no recorded consent for
	// this login session. Terminal: polling again cannot fix it.
	freePiPollConsentRequired
)

type freePiPollOutcome struct {
	Status     freePiPollStatus
	Token      string
	RetryAfter int
}

// String renders the poll state for logs.
func (s freePiPollStatus) String() string {
	switch s {
	case freePiPollGranted:
		return "granted"
	case freePiPollConsentRequired:
		return "consent_required"
	default:
		return "pending"
	}
}

// freePiPost sends a JSON POST and returns the status and body. A non-2xx is
// reported through status, not an error, because every caller here branches on
// the status (403 consent_required is a normal outcome, not a failure).
func freePiPost(ctx context.Context, path string, payload any, bearer string) (int, []byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal %s: %w", path, err)
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, freePiAuthBaseURL+path, body)
	if err != nil {
		return 0, nil, fmt.Errorf("build %s request: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := freePiClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read %s response: %w", path, err)
	}
	return resp.StatusCode, raw, nil
}

// freePiStartDeviceFlow opens a device-code login session.
func freePiStartDeviceFlow(r *http.Request) (freePiDeviceResponse, error) {
	status, raw, err := freePiPost(r.Context(), "/auth/github/device", map[string]any{}, "")
	if err != nil {
		return freePiDeviceResponse{}, err
	}
	if status != http.StatusOK {
		return freePiDeviceResponse{}, fmt.Errorf("device flow start failed: HTTP %d", status)
	}

	var out freePiDeviceResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return freePiDeviceResponse{}, fmt.Errorf("parse device flow response: %w", err)
	}
	if out.SessionID == "" {
		return freePiDeviceResponse{}, errors.New("device flow returned no session id")
	}
	// The server's own cadence wins; 5s is the CLI's fallback for a reply that
	// omits it (src/auth-flow.ts).
	if out.Interval <= 0 {
		out.Interval = 5
	}
	return out, nil
}

// freePiSubmitConsent records consent for one login session. Called only after
// the user accepted, never on their behalf.
func freePiSubmitConsent(r *http.Request, sessionID string) error {
	status, raw, err := freePiPost(r.Context(), "/auth/consent", map[string]any{
		"session_id":      sessionID,
		"consent_version": providers.FreePiConsentVersion,
	}, "")
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("consent submission failed: HTTP %d: %s", status, truncateBody(raw))
	}
	return nil
}

// freePiPollToken performs one poll of POST /auth/token.
func freePiPollToken(r *http.Request, sessionID string) (freePiPollOutcome, error) {
	status, raw, err := freePiPost(r.Context(), "/auth/token", map[string]any{
		"session_id": sessionID,
	}, "")
	if err != nil {
		return freePiPollOutcome{}, err
	}

	// Shapes observed against api.freepi.ai (2026-11-09):
	//   not authorised yet -> 200 {"status":"pending"}
	//   no consent recorded -> 403 {"code":"consent_required","message":"..."}
	//   authorised          -> 200 carrying a "token"
	// The `status` field is not in free-pi-cli's union schema, and the pending
	// body carries no `code` or `retry_after`, so both must be tolerated rather
	// than assumed. A body that will not parse is an error, not "pending": the
	// silent version of that mistake is an indefinite spinner.
	var body struct {
		Token      string `json:"token"`
		Code       string `json:"code"`
		Status     string `json:"status"`
		RetryAfter int    `json:"retry_after"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return freePiPollOutcome{}, fmt.Errorf("token poll: unparseable response: HTTP %d: %s", status, truncateBody(raw))
	}

	// A token wins over any status wording: that is the thing we came for, and
	// free-pi-cli itself decides on presence of the field rather than a label.
	if body.Token != "" {
		return freePiPollOutcome{Status: freePiPollGranted, Token: body.Token}, nil
	}
	if body.Code == providers.FreePiCodeConsentRequired ||
		body.Status == providers.FreePiCodeConsentRequired ||
		status == http.StatusForbidden {
		return freePiPollOutcome{Status: freePiPollConsentRequired}, nil
	}
	if status != http.StatusOK {
		return freePiPollOutcome{}, fmt.Errorf("token poll failed: HTTP %d: %s", status, truncateBody(raw))
	}
	return freePiPollOutcome{Status: freePiPollPending, RetryAfter: body.RetryAfter}, nil
}

// freePiResetSessions releases the account's other sessions.
func freePiResetSessions(r *http.Request, token string) (bool, error) {
	status, raw, err := freePiPost(r.Context(), "/session/reset", nil, token)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK {
		return false, fmt.Errorf("session reset failed: HTTP %d: %s", status, truncateBody(raw))
	}
	var out struct {
		Released bool `json:"released"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Released, nil
}

// freePiClientVersion is the subset of GET /client-version this provider uses.
type freePiClientVersion struct {
	Min    string `json:"min"`
	Latest string `json:"latest"`
	Model  string `json:"model"`
	Models []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"models"`
}

// freePiFetchClientVersion reads the server's current catalogue. It is
// unauthenticated and account-independent. Best-effort: a failure must not fail
// a login that already produced a token, so the caller falls back to the
// compiled-in floor and the default model.
func freePiFetchClientVersion(ctx context.Context) (freePiClientVersion, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, freePiAuthBaseURL+"/client-version", nil)
	if err != nil {
		return freePiClientVersion{}, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := freePiClient.Do(req)
	if err != nil {
		return freePiClientVersion{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return freePiClientVersion{}, fmt.Errorf("client-version: HTTP %d", resp.StatusCode)
	}

	var out freePiClientVersion
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return freePiClientVersion{}, fmt.Errorf("parse client-version: %w", err)
	}
	return out, nil
}

// freePiModelIDs flattens the catalogue into request model ids, dropping blanks
// so the dashboard never renders an unnamed row.
func freePiModelIDs(cv freePiClientVersion) []string {
	out := make([]string, 0, len(cv.Models))
	for _, m := range cv.Models {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out
}
