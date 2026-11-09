package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
	"time"

	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/usagetracker"
	json "encoding/json/v2"
)

// Codex OAuth transcription, ported from upstream
// open-sse/handlers/codexStt.js (refs 421f2262): the ChatGPT transcription
// endpoint is addressed with the Codex account's own identity, reusing the
// chat connection's OAuth token instead of an OpenAI API key. The endpoint is
// fixed — a connection URL override is never honored — and the response is
// limited to {text} or plain text.

const (
	codexSTTModel        = "transcribe"
	codexSTTMaxResponse  = 1024 * 1024
	codexSTTMaxText      = 250000
	codexSTTTimeout      = 60 * time.Second
	codexSTTConcurrency  = 2
	codexSTTFailedMsg    = "Codex transcription failed"
	codexSTTInvalidResp  = "Invalid transcription response"
	codexSTTModelHintMsg = "Use model cx/transcribe for Codex STT"
)

// codexSTTUpstream is the fixed ChatGPT transcription endpoint. A variable so
// the test suite can aim it at a fake upstream; production never rewrites it.
var codexSTTUpstream = "https://chatgpt.com/backend-api/transcribe"

// codexConvertAudio is the conversion seam the transport tests swap for a
// canned WAV; production always uses convertAudio.
var codexConvertAudio = convertAudio

// codexSTTSlots caps concurrent conversions+uploads; upstream answers 503
// past the second in-flight transcription.
var codexSTTSlots = make(chan struct{}, codexSTTConcurrency)

// Printable-ASCII token and account-id shapes, straight from upstream's
// validation: either failing means the stored OAuth data is not usable.
var (
	codexTokenRE    = regexp.MustCompile(`^[\x21-\x7e]+$`)
	codexAccountRE  = regexp.MustCompile(`^[\x21-\x7e]{1,256}$`)
	codexSTTFields  = map[string]bool{"model": true, "file": true, "response_format": true, "language": true, "prompt": true, "temperature": true}
	codexSTTNoGo    = []string{"language", "prompt", "temperature"}
	codexSTTFormats = map[string]bool{"json": true, "text": true}
)

// handleCodexSTT transcribes audio over a Codex OAuth connection, falling
// back across accounts like the antigravity lane does.
func (h *MediaHandler) handleCodexSTT(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	pinned := modelInfo.ConnectionID
	if pinned == "" {
		pinned = imagePinnedConnectionID(r)
	}
	usePinned := pinned != ""
	excludeIDs := []string{}
	var lastErr error
	for {
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), "codex", pinned, excludeIDs, codexSTTModel)
		if err != nil || conn == nil {
			if lastErr != nil {
				return lastErr
			}
			return fmt.Errorf("no active connection for codex: %w", err)
		}
		attemptErr := h.tryCodexSTTConn(w, r, body, conn, connData)
		if attemptErr == nil {
			return nil
		}
		lastErr = attemptErr
		if usePinned {
			return lastErr
		}
		// Bad audio and unsupported parameters are client errors, not broken
		// accounts (upstream 421f2262): they end the request instead of
		// burning the remaining accounts on the same refused body.
		var stErr *codexSTTError
		if errors.As(attemptErr, &stErr) && (stErr.status == 400 || stErr.status == 413 || stErr.status == 504) {
			return attemptErr
		}
		excludeIDs = append(excludeIDs, conn.ID)
	}
}

// tryCodexSTTConn runs one account's transcription end to end. A 401 gets one
// forced token refresh and a single retry before the account is given up on;
// client errors (bad audio, unsupported parameters) are answered as-is
// because they say nothing about the account's health.
func (h *MediaHandler) tryCodexSTTConn(w http.ResponseWriter, r *http.Request, body []byte, conn *models.ProviderConnection, connData *chat.ConnectionData) error {
	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		return fmt.Errorf("no API key found for codex connection %s", conn.ID)
	}
	refreshed, _, err := h.ChatH.RefreshOAuthTokenIfExpired(conn.ID, apiKey)
	if err == nil && refreshed != "" {
		apiKey = refreshed
	}

	form, err := parseCodexSTTForm(body, r.Header.Get("Content-Type"))
	if err != nil {
		return err
	}
	if err := validateCodexSTTForm(form); err != nil {
		return err
	}

	var accountID string
	if psd := connData.ProviderSpecificData; psd != nil {
		for _, key := range []string{"workspaceId", "chatgptAccountId", "accountId"} {
			if v, _ := psd[key].(string); v != "" {
				accountID = v
				break
			}
		}
	}
	if !codexTokenRE.MatchString(apiKey) || !codexAccountRE.MatchString(accountID) {
		return codexSTTFailure(401, "A valid Codex OAuth account is required")
	}

	select {
	case codexSTTSlots <- struct{}{}:
		defer func() { <-codexSTTSlots }()
	default:
		return codexSTTFailure(503, "Transcription capacity reached")
	}

	wav, err := codexConvertAudio(form.file)
	if err != nil {
		return err
	}

	resp, err := h.postCodexTranscribe(r.Context(), connData, apiKey, accountID, wav)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// One forced refresh and one retry: a token the lazy check considered
		// valid can still have been revoked upstream (upstream 421f2262).
		_ = resp.Body.Close()
		forced, _, ferr := h.ChatH.ForceRefreshOAuthToken(conn.ID)
		if ferr == nil && forced != "" && forced != apiKey {
			apiKey = forced
			resp, err = h.postCodexTranscribe(r.Context(), connData, apiKey, accountID, wav)
			if err != nil {
				return err
			}
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		h.lockCodexSTTAccount(r, resp.StatusCode, respBody, conn)
		status := resp.StatusCode
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		return codexSTTFailure(status, codexSTTFailedMsg)
	}

	text, err := readCodexSTTResponse(resp.Body)
	if err != nil {
		return err
	}

	if form.responseFormat == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(text))
	} else {
		w.Header().Set("Content-Type", "application/json")
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"text": text})
	}

	h.finishCodexSTTConn(conn)
	return nil
}

// codexSTTForm is the validated subset of the caller's multipart body.
type codexSTTForm struct {
	responseFormat string
	extras         map[string]string
	file           []byte
}

// parseCodexSTTForm reads the multipart body once. Upstream rejects duplicate
// and unknown fields outright: a second "file" part would otherwise silently
// win, and a typo'd parameter would silently vanish.
func parseCodexSTTForm(body []byte, contentType string) (*codexSTTForm, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		return nil, codexSTTFailure(400, "expected multipart/form-data")
	}
	seen := map[string]int{}
	form := &codexSTTForm{responseFormat: "json"}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, codexSTTFailure(400, "malformed multipart body")
		}
		name := p.FormName()
		seen[name]++
		if !codexSTTFields[name] || seen[name] > 1 {
			return nil, codexSTTFailure(400, "Unsupported or duplicate form field")
		}
		switch name {
		case "file":
			form.file, err = io.ReadAll(p)
			if err != nil {
				return nil, codexSTTFailure(400, "malformed multipart body")
			}
		default:
			value, _ := io.ReadAll(io.LimitReader(p, 1<<20))
			if name == "response_format" {
				form.responseFormat = strings.TrimSpace(string(value))
			} else {
				if form.extras == nil {
					form.extras = map[string]string{}
				}
				form.extras[name] = strings.TrimSpace(string(value))
			}
		}
	}
	return form, nil
}

// validateCodexSTTForm applies the parameter contract: only json/text out, no
// language/prompt/temperature, one non-empty file within the size cap.
func validateCodexSTTForm(form *codexSTTForm) error {
	if !codexSTTFormats[form.responseFormat] {
		return codexSTTFailure(400, "Only json and text response formats are supported")
	}
	for _, key := range codexSTTNoGo {
		if value, present := form.extras[key]; present && value != "" {
			return codexSTTFailure(400, fmt.Sprintf("%s is not supported by Codex transcription", key))
		}
	}
	if len(form.file) == 0 {
		return codexSTTFailure(400, "A non-empty audio file is required")
	}
	if len(form.file) > codexMaxAudioBytes {
		return codexSTTFailure(413, "Audio exceeds 25 MiB")
	}
	return nil
}

// postCodexTranscribe sends the file-only multipart request upstream. The
// pooled client already carries the connection's proxy when one is set, which
// is the Go consolidation of upstream's direct/proxied fetch split.
func (h *MediaHandler) postCodexTranscribe(ctx context.Context, connData *chat.ConnectionData, apiKey, accountID string, wav []byte) (*http.Response, error) {
	boundary := "----9router-codex-stt"
	var body bytes.Buffer
	fmt.Fprintf(&body, "--%s\r\nContent-Disposition: form-data; name=\"file\"; filename=\"audio.wav\"\r\nContent-Type: audio/wav\r\n\r\n", boundary)
	body.Write(wav)
	body.WriteString("\r\n--" + boundary + "--\r\n")

	ctx, cancel := context.WithTimeout(ctx, codexSTTTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, codexSTTUpstream, bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, codexSTTFailure(502, codexSTTFailedMsg)
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("ChatGPT-Account-Id", accountID)
	httpReq.Header.Set("User-Agent", providers.CodexCLIUserAgent)
	httpReq.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	httpReq.Header.Set("Content-Length", fmt.Sprint(body.Len()))

	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		return nil, clientErr
	}
	return client.Do(httpReq)
}

// readCodexSTTResponse bounds and validates the upstream answer: a JSON
// object whose text is a string within the length cap.
func readCodexSTTResponse(body io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(body, codexSTTMaxResponse+1))
	if err != nil {
		return "", codexSTTFailure(502, codexSTTFailedMsg)
	}
	if len(raw) > codexSTTMaxResponse {
		return "", codexSTTFailure(502, "Body exceeds size limit")
	}
	var data struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(raw, &data) != nil || data.Text == nil || len(*data.Text) > codexSTTMaxText {
		return "", codexSTTFailure(502, codexSTTInvalidResp)
	}
	return *data.Text, nil
}

// lockCodexSTTAccount cools an account down on a fallback-eligible failure,
// exactly like the antigravity STT lane does; client errors never reach here.
func (h *MediaHandler) lockCodexSTTAccount(r *http.Request, status int, respBody []byte, conn *models.ProviderConnection) {
	if h.Repo == nil || conn == nil {
		return
	}
	log.Warn("media", "codex stt upstream error", "status", status, "body", string(respBody[:min(300, len(respBody))]))
	if handlerutil.IsProbeContext(r.Context()) {
		return
	}
	if classification := providers.ClassifyError(status, string(respBody), h.Repo.GetConnectionBackoffLevel(conn.ID)); classification.ShouldFallback {
		cooldownSec := max(classification.CooldownMs/1000, 1)
		_ = h.Repo.LockConnectionModel(conn.ID, codexSTTModel, cooldownSec, classification.NewBackoffLevel)
	}
}

func (h *MediaHandler) finishCodexSTTConn(conn *models.ProviderConnection) {
	if h.Repo != nil && conn != nil {
		_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
	}
	usagetracker.GetTracker().PushRecent(usagetracker.RecentRequest{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Model:     codexSTTModel,
		Provider:  "codex",
		Status:    "ok",
	}, h.Repo)
}
