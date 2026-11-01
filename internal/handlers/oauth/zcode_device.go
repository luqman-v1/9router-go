package oauth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// zcodeUUID returns a random RFC 4122 v4 UUID — the shape every identity
// field on this lane (deviceId, sessionSeed) must have.
func zcodeUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// zcodeOAuthOrigin returns the Z.ai OAuth base URL (overridable via env).
func zcodeOAuthOrigin() string {
	if v := strings.TrimSpace(os.Getenv("ZCODE_OAUTH_ORIGIN")); v != "" {
		return v
	}
	return "https://zcode.z.ai"
}

// zcodeStart begins a ZCode device-code (CLI) OAuth flow.
// Port of bridge auth.js:startDeviceFlow. Returns the standard deviceStart shape
// so the existing HandleDeviceStart / HandleDevicePoll frontend path works as-is.
//
// ZCode's flow differs from standard OAuth device flows: there is no user_code
// (the user approves via browser → no code to type). verification_uri_complete
// is the authorizeUrl the gateway returns and the user opens in any browser.
// The flowId + pollToken are threaded through the session map.
// zcodeStartContext begins a ZCode device-code (CLI) OAuth flow. The context
// is the caller's request context, so a cancelled login stops dialling.
func zcodeStartContext(ctx context.Context, provider string) (map[string]any, error) {
	origin := zcodeOAuthOrigin()
	// 32 random bytes hex-encoded (64 chars) — same shape as the bridge's
	// crypto.randomBytes(32).toString('hex'); the gateway rejects shorter
	// tokens with invalid_flow.
	pollBytes := make([]byte, 32)
	if _, err := rand.Read(pollBytes); err != nil {
		return nil, fmt.Errorf("poll token entropy: %w", err)
	}
	pollToken := hex.EncodeToString(pollBytes)

	payload, _ := json.Marshal(map[string]any{"provider": "zai"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/api/v1/oauth/cli/init",
		strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+pollToken)

	resp, err := deviceClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ZCode device init failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var out struct {
		Data *struct {
			FlowID       string  `json:"flow_id"`
			AuthorizeURL string  `json:"authorize_url"`
			ExpiresAt    float64 `json:"expires_at"`
			IntervalSec  float64 `json:"poll_interval_sec"`
			PollToken    string  `json:"poll_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Data == nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ZCode device init failed (HTTP %d): %s", resp.StatusCode, firstN(string(raw), 200))
	}
	d := out.Data
	if d.FlowID == "" || d.AuthorizeURL == "" {
		return nil, fmt.Errorf("ZCode device init: missing flow_id or authorize_url in response")
	}

	returnedPollToken := d.PollToken
	if returnedPollToken == "" {
		returnedPollToken = pollToken
	}
	intervalSec := int(d.IntervalSec)
	if intervalSec < 2 {
		intervalSec = 2
	}
	expiresIn := 300 // default 5 min
	if d.ExpiresAt > 0 {
		remaining := int(d.ExpiresAt - float64(time.Now().Unix()))
		if remaining > 0 {
			expiresIn = remaining
		}
	}

	// Standard device-flow shape the frontend (ProviderDetailView) expects.
	// device_code = flowId (opaque polling key)
	// user_code = "" (ZCode has no short user-typeable code)
	// verification_uri_complete = the full authorize URL the user opens
	return map[string]any{
		"device_code":               d.FlowID,
		"user_code":                 "",
		"verification_uri":          d.AuthorizeURL,
		"verification_uri_complete": d.AuthorizeURL,
		"expires_in":                expiresIn,
		"interval":                  intervalSec,
		"session": map[string]any{
			"pollToken": returnedPollToken,
			"provider":  provider,
		},
	}, nil
}

// zcodePoll polls one tick of a ZCode device flow.
// code = flowId (device_code from zcodeStart).
// session must contain "pollToken".
// zcodePollContext polls one tick of a ZCode device flow.
func zcodePollContext(ctx context.Context, provider, flowID string, session map[string]any) (deviceTokens, error) {
	var t deviceTokens
	pollToken, _ := session["pollToken"].(string)
	if pollToken == "" {
		return t, fmt.Errorf("missing pollToken in session")
	}

	origin := zcodeOAuthOrigin()
	pollURL := origin + "/api/v1/oauth/cli/poll/" + flowID

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
	if err != nil {
		return t, fmt.Errorf("build poll request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+pollToken)

	resp, err := deviceClient.Do(req)
	if err != nil {
		return t, fmt.Errorf("poll request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var out struct {
		Data *struct {
			Status string         `json:"status"`
			Token  string         `json:"token"`
			User   map[string]any `json:"user"`
			Zai    *struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
			} `json:"zai"`
			Bigmodel *struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
			} `json:"bigmodel"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Data == nil {
		return t, fmt.Errorf("poll response parse failed: %s", firstN(string(raw), 200))
	}

	d := out.Data
	switch d.Status {
	case "failed", "expired":
		return t, fmt.Errorf("zcode flow %s", d.Status)
	case "ready":
		// fall through
	default:
		// pending — return empty tokens, caller treats empty access as pending
		return t, nil
	}

	jwt := strings.TrimSpace(d.Token)
	if jwt == "" {
		return t, fmt.Errorf("zcode: ready but no plan JWT in response")
	}

	var refreshToken string
	if d.Zai != nil {
		refreshToken = d.Zai.RefreshToken
	} else if d.Bigmodel != nil {
		refreshToken = d.Bigmodel.RefreshToken
	}

	var email, accountUUID string
	if d.User != nil {
		email, _ = d.User["email"].(string)
		accountUUID, _ = d.User["user_id"].(string)
	}

	// The plan lane fingerprints the caller, so the identity has to survive the
	// login: a device id that changes per request (or a session seed shared by
	// every account) is what the gateway answers with 3012. The standalone
	// bridge seeds both per account in its store; carry them on the connection.
	deviceID := zcodeUUID()
	sessionSeed := zcodeUUID()
	t.extra = map[string]any{
		"deviceId":    deviceID,
		"sessionSeed": sessionSeed,
	}
	if accountUUID != "" {
		t.extra["accountUUID"] = accountUUID
	}

	// access = plan JWT (executor reads this as Bearer token)
	t.access = jwt
	t.refresh = refreshToken
	t.email = email
	// no expiresIn: the plan JWT carries no exp claim; a revoked
	// token_version is what ends its life, and that surfaces as a 401.
	return t, nil
}

// zcodeExistingIdentity finds the connection already stored for this z.ai
// account. Re-login mints a fresh plan JWT, so the row must be reused by
// account (email) rather than by credential hash: the new token bumps
// token_version and leaves the credential-keyed row permanently unusable.
// Returns the row id and its providerSpecificData (deviceId, sessionSeed,
// accountUUID) to preserve.
func zcodeExistingIdentity(ctx context.Context, db *sql.DB, email string) (string, map[string]any) {
	email = strings.TrimSpace(email)
	if db == nil || email == "" {
		return "", nil
	}
	rows, err := db.QueryContext(ctx,
		"SELECT id, data FROM providerConnections WHERE provider = 'zcode' AND isActive = 1 ORDER BY updatedAt DESC")
	if err != nil {
		return "", nil
	}
	defer func() { _ = rows.Close() }()
	var bestID string
	var bestPSD map[string]any
	for rows.Next() {
		var id, data string
		if rows.Scan(&id, &data) != nil {
			continue
		}
		var parsed struct {
			Email string         `json:"email"`
			PSD   map[string]any `json:"providerSpecificData"`
		}
		if json.Unmarshal([]byte(data), &parsed) != nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(parsed.Email), email) {
			continue
		}
		bestID = id
		bestPSD = parsed.PSD
		break
	}
	if err := rows.Err(); err != nil {
		return "", nil
	}
	return bestID, bestPSD
}
