package media

import (
	"bytes"
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/log"
)

// veoaifree.com free media generator, ported from upstream
// open-sse/executors/veoaifree-web.js (38a0c449, part of #624): a WordPress
// AJAX API with no credentials (rate limited to ~6 requests/hour per IP).
// Video: full-video-generate → final-video-results polling → mp4 artifact
// download. Image: the nano-banana generator answering URLs or raw base64
// parts. Every artifact host is resolved and checked against private
// ranges before the download, and the mp4 magic is verified.

const (
	veoFreeMaxPolls     = 30 // 10 minutes
	veoFreeFetchTimeout = 30 * time.Second
	veoFreeMaxArtifact  = 100 << 20
	veoFreeMaxRedirects = 3
	veoFreeReadyTries   = 12
	veoFreeUA           = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36"
)

// The endpoints are fixed HTTPS origins in production and the poll cadence
// is a 20s gap; vars so the test suite can aim them at a fake site and run
// the polling loop fast. veoFreeAllowPrivateHosts exists because those fakes
// are loopback servers the SSRF guard would otherwise refuse.
var (
	veoFreePollInterval      = 20 * time.Second
	veoFreeReadyWait         = 5 * time.Second
	veoFreeBaseURL           = "https://veoaifree.com"
	veoFreeAJAXURL           = "https://veoaifree.com/wp-admin/admin-ajax.php"
	veoFreeAllowPrivateHosts bool
	veoFreeClient            = &http.Client{
		Timeout: veoFreeFetchTimeout,
		// Redirects are followed manually for artifact downloads; the AJAX
		// calls never redirect.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
)

var veoFreeNonceRE = regexp.MustCompile(`nonce":"([a-f0-9]+)"`)

// veoFreeSession carries the cookie jar the AJAX flow accumulates.
type veoFreeSession struct {
	cookies map[string]string
}

func (s *veoFreeSession) cookieHeader() string {
	parts := make([]string, 0, len(s.cookies))
	for name, value := range s.cookies {
		parts = append(parts, name+"="+value)
	}
	return strings.Join(parts, "; ")
}

func (s *veoFreeSession) merge(resp *http.Response) {
	for _, cookie := range resp.Cookies() {
		if cookie.Value != "" {
			if s.cookies == nil {
				s.cookies = map[string]string{}
			}
			s.cookies[cookie.Name] = cookie.Value
		}
	}
}

// veoFreeDo issues one session request with the browser identity headers.
func veoFreeDo(ctx context.Context, session *veoFreeSession, method, target string, body io.Reader, extraHeaders map[string]string) (*http.Response, error) {
	callCtx, cancel := context.WithTimeout(ctx, veoFreeFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", veoFreeUA)
	req.Header.Set("Referer", veoFreeBaseURL+"/")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	if cookie := session.cookieHeader(); cookie != "" && req.Header.Get("Cookie") == "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := veoFreeClient.Do(req)
	if err != nil {
		return nil, err
	}
	session.merge(resp)
	return resp, nil
}

// veoFreeFetchNonce scrapes the CSRF nonce off the homepage.
func veoFreeFetchNonce(ctx context.Context, session *veoFreeSession) (string, error) {
	resp, err := veoFreeDo(ctx, session, http.MethodGet, veoFreeBaseURL, nil, nil)
	if err != nil {
		return "", fmt.Errorf("veoaifree homepage fetch failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	html, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return "", err
	}
	m := veoFreeNonceRE.FindSubmatch(html)
	if m == nil {
		return "", errors.New("failed to extract CSRF nonce from veoaifree.com (site layout changed or rate limited)")
	}
	return string(m[1]), nil
}

// veoFreePostAJAX posts one admin-ajax action.
func veoFreePostAJAX(ctx context.Context, session *veoFreeSession, nonce string, params map[string]string) (string, error) {
	form := url.Values{}
	form.Set("action", "veo_video_generator")
	form.Set("nonce", nonce)
	for k, v := range params {
		form.Set(k, v)
	}
	resp, err := veoFreeDo(ctx, session, http.MethodPost, veoFreeAJAXURL, strings.NewReader(form.Encode()), map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
		"Origin":       veoFreeBaseURL,
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// veoFreeRejected matches upstream's "0"/error sniff on AJAX answers.
func veoFreeRejected(result string) bool {
	trimmed := strings.TrimSpace(result)
	lower := strings.ToLower(trimmed)
	return trimmed == "" || trimmed == "0" || strings.Contains(lower, "error")
}

// veoFreeAspect maps the size parameter onto the plugin's enum values.
func veoFreeAspect(size, prefix string) string {
	value := strings.ToLower(size)
	switch {
	case strings.Contains(value, "9:16") || strings.Contains(value, "portrait"):
		return prefix + "_PORTRAIT"
	case strings.Contains(value, "1:1") || strings.Contains(value, "square"):
		return prefix + "_SQUARE"
	default:
		return prefix + "_LANDSCAPE"
	}
}

// veoFreeArtifactNotReady marks a retryable artifact fetch (202/404/409/425).
type veoFreeArtifactNotReady struct{ status int }

func (e *veoFreeArtifactNotReady) Error() string {
	return fmt.Sprintf("artifact not available yet (HTTP %d)", e.status)
}

// veoFreeIsPrivateAddr mirrors upstream's private-range list, IPv4 and IPv6.
func veoFreeIsPrivateAddr(addr string) bool {
	raw := strings.ToLower(strings.Trim(addr, "[]"))
	if mapped, ok := strings.CutPrefix(raw, "::ffff:"); ok {
		raw = mapped
	}
	ip := net.ParseIP(raw)
	if ip == nil {
		return true // unparseable is not a risk worth taking
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	return false
}

// veoFreeAssertPublicHost refuses artifact hosts that are or resolve to
// private addresses — the AJAX answers are attacker-controllable strings, so
// the download must not become an SSRF hop into the operator's network.
func veoFreeAssertPublicHost(ctx context.Context, hostname string) error {
	if veoFreeAllowPrivateHosts {
		return nil
	}
	bare := strings.Trim(hostname, "[]")
	if ip := net.ParseIP(bare); ip != nil {
		if veoFreeIsPrivateAddr(bare) {
			return errors.New("artifact host is a blocked address")
		}
		return nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", bare)
	if err != nil || len(ips) == 0 {
		return errors.New("artifact host could not be resolved")
	}
	for _, ip := range ips {
		if veoFreeIsPrivateAddr(ip.String()) {
			return errors.New("artifact host resolves to a blocked address")
		}
	}
	return nil
}

// veoFreeDownload fetches the artifact, following at most three redirects
// manually so every hop re-runs the public-host check, and verifying the
// mp4 magic before handing the bytes back.
func veoFreeDownload(ctx context.Context, session *veoFreeSession, artifactURL string) ([]byte, error) {
	current := artifactURL
	for redirect := 0; redirect <= veoFreeMaxRedirects; redirect++ {
		parsed, err := url.Parse(current)
		if err != nil {
			return nil, err
		}
		if parsed.Scheme != "https" {
			return nil, errors.New("artifact URL must use HTTPS")
		}
		if err := veoFreeAssertPublicHost(ctx, parsed.Hostname()); err != nil {
			return nil, err
		}
		resp, err := veoFreeDo(ctx, session, http.MethodGet, current, nil, nil)
		if err != nil {
			return nil, err
		}
		switch {
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			location := resp.Header.Get("Location")
			if location == "" {
				_ = resp.Body.Close()
				return nil, errors.New("artifact redirect is missing a destination")
			}
			next, err := parsed.Parse(location)
			if err != nil {
				_ = resp.Body.Close()
				return nil, err
			}
			_ = resp.Body.Close()
			current = next.String()
			continue
		case resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNotFound ||
			resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusTooEarly:
			_ = resp.Body.Close()
			return nil, &veoFreeArtifactNotReady{status: resp.StatusCode}
		case resp.StatusCode != http.StatusOK:
			_ = resp.Body.Close()
			return nil, fmt.Errorf("artifact download failed (HTTP %d)", resp.StatusCode)
		}
		contentType := strings.ToLower(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
		if contentType != "video/mp4" && contentType != "application/octet-stream" {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("artifact returned unsupported content type (%s)", contentType)
		}
		buf, err := io.ReadAll(io.LimitReader(resp.Body, veoFreeMaxArtifact+1))
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if len(buf) > veoFreeMaxArtifact {
			return nil, errors.New("artifact exceeds 100MB limit")
		}
		if len(buf) < 12 || !bytes.Equal(buf[4:8], []byte("ftyp")) {
			return nil, errors.New("artifact is not a valid MP4 file")
		}
		return buf, nil
	}
	return nil, errors.New("artifact exceeded the redirect limit")
}

// veoFreeDownloadWithRetry keeps retrying the not-yet-ready statuses.
func veoFreeDownloadWithRetry(ctx context.Context, session *veoFreeSession, artifactURL string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < veoFreeReadyTries; attempt++ {
		buf, err := veoFreeDownload(ctx, session, artifactURL)
		if err == nil {
			return buf, nil
		}
		var notReady *veoFreeArtifactNotReady
		if !errors.As(err, &notReady) {
			return nil, err
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(veoFreeReadyWait):
		}
	}
	return nil, lastErr
}

// handleVeoFreeWebImage serves /v1/images/generations for veoaifree-web: the
// nano-banana generator answering URLs or raw base64 parts.
func (h *MediaHandler) handleVeoFreeWebImage(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	var req struct {
		Prompt string `json:"prompt"`
		Size   string `json:"size"`
	}
	_ = json.Unmarshal(body, &req)
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return errors.New("missing required field: prompt")
	}
	session := &veoFreeSession{}
	nonce, err := veoFreeFetchNonce(r.Context(), session)
	if err != nil {
		return err
	}
	result, err := veoFreePostAJAX(r.Context(), session, nonce, map[string]string{
		"promptIMG":          prompt,
		"totalVariationsIMG": "1",
		"aspectRatioIMG":     veoFreeAspect(req.Size, "IMAGE_ASPECT_RATIO"),
		"actionType":         "banan-image-generator",
	})
	if err != nil {
		return err
	}
	if veoFreeRejected(result) {
		return fmt.Errorf("veoaifree image generation failed: %s", firstN(strings.TrimSpace(result), 120))
	}
	data := make([]any, 0)
	for _, part := range strings.Split(strings.TrimSpace(result), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "http://") || strings.HasPrefix(part, "https://") {
			data = append(data, map[string]any{"url": part})
		} else {
			data = append(data, map[string]any{"b64_json": part, "mime_type": "image/png"})
		}
	}
	if len(data) == 0 {
		return errors.New("veoaifree image generation returned no artifacts")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	out, _ := json.Marshal(map[string]any{"created": time.Now().Unix(), "data": data})
	_, _ = w.Write(out)
	return nil
}

// handleVeoFreeWebVideo serves /v1/videos/generations for veoaifree-web:
// submit, poll final-video-results, then download the mp4.
func (h *MediaHandler) handleVeoFreeWebVideo(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	var req struct {
		Prompt string `json:"prompt"`
		Size   string `json:"size"`
	}
	_ = json.Unmarshal(body, &req)
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return errors.New("missing required field: prompt")
	}
	session := &veoFreeSession{}
	nonce, err := veoFreeFetchNonce(r.Context(), session)
	if err != nil {
		return err
	}
	genResult, err := veoFreePostAJAX(r.Context(), session, nonce, map[string]string{
		"prompt":          prompt,
		"totalVariations": "1",
		"aspectRatio":     veoFreeAspect(req.Size, "VIDEO_ASPECT_RATIO"),
		"actionType":      "full-video-generate",
	})
	if err != nil {
		return err
	}
	if veoFreeRejected(genResult) {
		return fmt.Errorf("veoaifree video submit failed: %s", firstN(strings.TrimSpace(genResult), 120))
	}
	sceneData := strings.TrimSpace(genResult)

	for poll := 0; poll < veoFreeMaxPolls; poll++ {
		select {
		case <-r.Context().Done():
			return r.Context().Err()
		case <-time.After(veoFreePollInterval):
		}
		pollResult, err := veoFreePostAJAX(r.Context(), session, nonce, map[string]string{
			"sceneData":  sceneData,
			"actionType": "final-video-results",
		})
		if err != nil || veoFreeRejected(pollResult) {
			continue
		}
		artifactURL := ""
		for _, part := range strings.FieldsFunc(pollResult, func(r rune) bool { return r == ',' || r == '\n' }) {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "http://") || strings.HasPrefix(part, "https://") {
				artifactURL = part
				break
			}
		}
		if artifactURL == "" {
			continue
		}
		buf, err := veoFreeDownloadWithRetry(r.Context(), session, artifactURL)
		if err != nil {
			return err
		}
		log.Info("media", "veoaifree video ready", "polls", poll+1)
		return writeUcMediaJSON(w, base64.StdEncoding.EncodeToString(buf), "video/mp4")
	}
	return errors.New("veoaifree video generation timed out after 10 minutes")
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
