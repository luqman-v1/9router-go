package executor

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/proxy/oauth"
)

// autoclawAppKey is the app secret the desktop client signs each request
// with. It rotates when the desktop app bumps its build; a 403 on every
// request means it changed and must be re-dumped from the client.
const (
	autoclawAppID    = "100003"
	autoclawAppKey   = "38d2391985e2369a5fb8227d8e6cd5e5"
	autoclawVersion  = "1.18.5"
	autoclawChatPath = "/autoclaw-proxy/proxy/autoclaw/chat/completions"
	autoclawBaseURL  = "https://autoglm-api.autoglm.ai"
)

// now returns unix seconds; a test seam so the signer can be frozen.
var now = func() int64 { return time.Now().Unix() }

// autoclawExecutorBaseURL is a var so tests can point it at an httptest
// server. Production value is the real upstream host.
var autoclawExecutorBaseURL = autoclawBaseURL

// ForwardAutoclaw signs each request with the app's MD5 scheme, moves the
// model into X-Request-Model (the body's `model` field is ignored upstream),
// forces stream:true, and relays the SSE response. The access token is
// rotated from the durable refresh_token|device_id stored in the connection's
// apiKey field via oauth.Refresh, which is dedup-cached so a hot connection
// does not hit the token endpoint on every turn.
func ForwardAutoclaw(w http.ResponseWriter, req *Request) error {
	refreshToken, deviceID := splitAutoclawCredential(req.APIKey)

	result, err := oauth.Refresh(req.Ctx, &oauth.Params{
		Provider:             "autoclaw",
		RefreshToken:         refreshToken,
		AccessToken:          "",
		ProviderSpecificData: map[string]string{"device_id": deviceID},
	})
	if err != nil {
		return fmt.Errorf("autoclaw: rotate access token: %w", err)
	}
	accessToken := result.AccessToken

	body, err := autoclawBody(req.Body, req.IsStream)
	if err != nil {
		return fmt.Errorf("autoclaw: prepare body: %w", err)
	}

	headers := autoclawHeaders(accessToken, req.IsStream, req.ModelName)

	resp, err := proxy.DoRequest(req.Ctx, req.Client, http.MethodPost, autoclawExecutorBaseURL+autoclawChatPath, headers, body)
	if err != nil {
		return fmt.Errorf("autoclaw: upstream: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// A 401 means the rotated access token was already stale (the background
	// refresher may not have ticked yet). Force one refresh and retry once
	// instead of surfacing a hard auth error.
	if resp.StatusCode == http.StatusUnauthorized {
		if err := resp.Body.Close(); err != nil {
			return fmt.Errorf("autoclaw: close response body: %w", err)
		}
		result, err = oauth.Refresh(req.Ctx, &oauth.Params{
			Provider:             "autoclaw",
			RefreshToken:         refreshToken,
			AccessToken:          accessToken,
			Force:                true,
			ProviderSpecificData: map[string]string{"device_id": deviceID},
		})
		if err != nil {
			return fmt.Errorf("autoclaw: force refresh after 401: %w", err)
		}
		headers = autoclawHeaders(result.AccessToken, req.IsStream, req.ModelName)
		resp, err = proxy.DoRequest(req.Ctx, req.Client, http.MethodPost, autoclawExecutorBaseURL+autoclawChatPath, headers, body)
		if err != nil {
			return fmt.Errorf("autoclaw: upstream retry: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
	}

	forwardUpstreamResponseHeaders(w, resp.Header)

	if req.IsStream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		_, copyErr := io.Copy(w, resp.Body)
		return copyErr
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("autoclaw: read non-stream response: %w", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, writeErr := w.Write(raw)
	return writeErr
}

// splitAutoclawCredential splits "refresh_token|device_id" into its parts.
// A bare refresh token (no device id) is also accepted.
func splitAutoclawCredential(cred string) (refreshToken, deviceID string) {
	parts := strings.SplitN(cred, "|", 2)
	refreshToken = strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		deviceID = strings.TrimSpace(parts[1])
	}
	return
}

// autoclawBody forces stream:true so the upstream keeps the SSE channel open,
// but only when the client asked for a stream. A non-stream request keeps the
// client's stream flag so the SSE body is never mislabelled as JSON.
// The body's `model` field is left as the client sent it; upstream reads the
// model from X-Request-Model instead.
func autoclawBody(raw []byte, isStream bool) ([]byte, error) {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if isStream {
		envelope["stream"] = true
	}
	return json.Marshal(envelope)
}

// autoclawHeaders builds the per-request signing envelope. The timestamp and
// MD5 signature change on every call, so they are never StaticHeaders.
func autoclawHeaders(accessToken string, isStream bool, model string) map[string]string {
	ts := fmt.Sprintf("%d", now())
	sign := md5Sum(autoclawAppID + "&" + ts + "&" + autoclawAppKey)

	headers := map[string]string{
		"Content-Type":     "application/json",
		"Accept":           "*/*",
		"x-auth-appid":     autoclawAppID,
		"x-auth-timestamp": ts,
		"x-auth-sign":      sign,
		"x-product":        "autoclaw",
		"x-version":        autoclawVersion,
		"x-tm":             "win",
		"x-client-type":    "pc",
		"x-channel":        "official",
		"x-lang":           "zh-CN",
		"x-trace-id":       mustUUID(),
		"User-Agent":       constants.DefaultClientUserAgent,
		"Authorization":    "Bearer " + accessToken,
	}
	if model != "" {
		headers["X-Request-Model"] = model
	}
	if isStream {
		headers["Accept"] = "text/event-stream"
		headers["Accept-Encoding"] = "identity"
	}
	return headers
}

func md5Sum(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// mustUUID returns a random UUID string. uuid is a direct go.mod dependency.
func mustUUID() string {
	return uuid.New().String()
}
