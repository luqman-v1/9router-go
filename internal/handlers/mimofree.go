package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/constants"
)

// MiMo anti-abuse: the free chat endpoint returns 403 "Illegal access"
// unless a system message contains this exact marker signature.
const mimoSystemMarker = "You are MiMoCode, an interactive CLI tool that helps users with software engineering tasks."

const mimoBootstrapURL = "https://api.xiaomimimimo.com/api/free-ai/bootstrap"
const mimoChatURL = "https://api.xiaomimimimo.com/api/free-ai/openai/chat"

const sessionIDLength = 24
const sessionAffixPrefix = "ses_"
const sessionChars = "abcdefghijklmnopqrstuvwxyz0123456789"

var (
	mimoJWT     string
	mimoJWTExp  time.Time
	mimoJWTMu   sync.Mutex
	mimoSession string
	mimoOnce    sync.Once
)

func getMimoSessionID() string {
	mimoOnce.Do(func() {
		var sb strings.Builder
		sb.WriteString(sessionAffixPrefix)
		for i := 0; i < sessionIDLength; i++ {
			sb.WriteByte(sessionChars[rand.Intn(len(sessionChars))])
		}
		mimoSession = sb.String()
	})
	return mimoSession
}

func generateMimoFingerprint() string {
	host, _ := os.Hostname()
	username := "unknown"
	if u := os.Getenv("USER"); u != "" {
		username = u
	}
	seed := fmt.Sprintf("%s|%s|%s", host, "mimo-free", username)
	h := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("%x", h)
}

// MimoFreeChat is an HTTP handler that forwards a request via the MiMo free tier.
// It bootstraps a JWT if needed, injects the anti-abuse system message marker,
// and forwards the request to the MiMo free endpoint.
func (h *ChatHandler) MimoFreeChat(w http.ResponseWriter, body []byte, isStream bool, metrics *streamMetrics) error {
	jwt, err := getMimoJWT()
	if err != nil {
		return fmt.Errorf("mimo bootstrap: %w", err)
	}

	upstreamBody := injectMimoMarker(body)

	req, err := http.NewRequest("POST", mimoChatURL, bytes.NewReader(upstreamBody))
	if err != nil {
		return fmt.Errorf("mimo request: %w", err)
	}

	req.Header.Set(constants.HeaderContentType, constants.ContentTypeJSON)
	req.Header.Set(constants.HeaderAuthorization, constants.AuthPrefixBearer+jwt)
	req.Header.Set("X-Mimo-Source", "mimocode-cli-free")
	req.Header.Set("x-session-affinity", getMimoSessionID())
	req.Header.Set(constants.HeaderUserAgent, "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	if isStream {
		req.Header.Set("Accept", "text/event-stream")
	}

	resp, err := h.Client.Do(req)
	if err != nil {
		return fmt.Errorf("mimo upstream: %w", err)
	}

	// On auth failure, re-bootstrap and retry once
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		mimoJWTMu.Lock()
		mimoJWT = ""
		mimoJWTExp = time.Time{}
		mimoJWTMu.Unlock()

		jwt, err = getMimoJWT()
		if err != nil {
			return fmt.Errorf("mimo re-bootstrap: %w", err)
		}
		req.Header.Set(constants.HeaderAuthorization, constants.AuthPrefixBearer+jwt)
		resp, err = h.Client.Do(req)
		if err != nil {
			return fmt.Errorf("mimo retry: %w", err)
		}
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return &upstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	if isStream {
		h.handleStreamResponse(w, resp.Body, false, time.Now(), metrics)
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.Copy(w, resp.Body)
	}

	return nil
}

// getMimoJWT returns a valid JWT, bootstrapping one if necessary.
func getMimoJWT() (string, error) {
	mimoJWTMu.Lock()
	defer mimoJWTMu.Unlock()

	if mimoJWT != "" && time.Until(mimoJWTExp) > 5*time.Minute {
		return mimoJWT, nil
	}

	fingerprint := generateMimoFingerprint()
	bootstrapBody, _ := json.Marshal(map[string]string{"client": fingerprint})

	resp, err := http.Post(mimoBootstrapURL, "application/json", bytes.NewReader(bootstrapBody))
	if err != nil {
		return "", fmt.Errorf("bootstrap request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bootstrap status: %d", resp.StatusCode)
	}

	var result struct {
		JWT string `json:"jwt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("bootstrap decode: %w", err)
	}
	if result.JWT == "" {
		return "", fmt.Errorf("bootstrap returned empty JWT")
	}

	mimoJWT = result.JWT
	mimoJWTExp = time.Now().Add(50 * time.Minute)

	return mimoJWT, nil
}

// injectMimoMarker ensures the request body has the anti-abuse system message.
func injectMimoMarker(body []byte) []byte {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	messages, ok := req["messages"].([]any)
	if !ok {
		return body
	}

	hasMarker := false
	for _, m := range messages {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := msg["role"].(string); role == "system" {
			if content, _ := msg["content"].(string); strings.Contains(content, mimoSystemMarker) {
				hasMarker = true
				break
			}
		}
	}

	if hasMarker {
		return body
	}

	markerMsg := map[string]any{
		"role":    "system",
		"content": mimoSystemMarker,
	}
	newMessages := append([]any{markerMsg}, messages...)
	req["messages"] = newMessages

	patched, _ := json.Marshal(req)
	return patched
}
