package executor

import (
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/log"
)

// Grok Imagine media generation, ported from the generateMedia half of
// upstream open-sse/executors/grok-web.js (38a0c449, part of #624): image and
// video turns go through the same conversations/new endpoint as chat, with
// image generation enabled or the imagine-video-gen payload, and the finished
// asset is downloaded from assets.grok.com as base64.
//
// The video lane is one iteration by design: video quota is ~1/day even on
// paid tiers, so looping video requests only burns quota.

// GrokMediaResult is one finished asset.
type GrokMediaResult struct {
	B64  string
	Mime string
}

// grokVideoResolution: Imagine's free tier renders 480p; 720p exists on paid
// quota keys but the 480p request is what a logged-out-of-SuperGrok account
// reliably gets.
const grokVideoResolution = "480p"

var grokAspectRatioRE = regexp.MustCompile(`^\d{1,2}:\d{1,2}$`)

// grokVideoPayload is the imagine-video-gen submit, captured from
// grok.com/imagine (upstream buildGrokVideoPayload).
func grokVideoPayload(prompt, size string, duration float64) grokMessagePayload {
	aspectRatio := "16:9"
	if size != "" && grokAspectRatioRE.MatchString(size) {
		aspectRatio = size
	}
	if duration <= 0 {
		duration = 6
	}
	return grokMessagePayload{
		Temporary:                   true,
		ModelName:                   "imagine-video-gen",
		Message:                     prompt + " --mode=custom",
		FileAttachments:             []any{},
		ImageAttachments:            []any{},
		DisableSearch:               false,
		EnableImageStreaming:        true,
		EnableSideBySide:            true,
		SendFinalMetadata:           true,
		ToolOverrides:               map[string]any{},
		IsReasoning:                 false,
		DisableTextFollowUps:        false,
		DisableMemory:               true,
		ForceSideBySide:             false,
		IsAsyncChat:                 false,
		DisableSelfHarmShortCircuit: false,
		DeviceEnvInfo:               grokDeviceEnvInfo(),
		ResponseMetadata:            map[string]any{"experiments": []any{}, "modelConfigOverride": map[string]any{"modelMap": map[string]any{}}},
		MediaGenInput: map[string]any{
			"textToVideo": map[string]any{
				"prompt":         prompt,
				"aspectRatio":    aspectRatio,
				"duration":       duration,
				"resolutionName": grokVideoResolution,
			},
		},
		Kind: "CONVERSATION_KIND_IMAGINE",
	}
}

// grokImagePayload is an image-generation chat turn: the model renders inline
// attachments, and the final one arrives as a cardAttachment JSON-line chunk.
func grokImagePayload(prompt, size string) grokMessagePayload {
	message := "Generate an image: " + prompt
	if size != "" {
		message = fmt.Sprintf("Generate an image (%s): %s", size, prompt)
	}
	payload := grokChatPayload(grokModelMap["grok-4.1-fast"], message)
	payload.EnableImageGeneration = true
	payload.EnableImageStreaming = true
	payload.ImageGenerationCount = 1
	return payload
}

var grokMp4URLRE = regexp.MustCompile(`\.mp4(\?|#|$)`)

// grokFindVideoURL deep-scans the NDJSON reply for the finished video asset.
// The exact chunk shape has not been pinned (it varies by client build), so
// any mp4 URL or relative path is collected from nested JSON — including
// cardAttachment.jsonData-style stringified payloads. Upstream keeps the last
// hit; so does this.
func grokFindVideoURL(text string) string {
	var last string
	var visit func(value any)
	visit = func(value any) {
		switch typed := value.(type) {
		case string:
			if grokMp4URLRE.MatchString(typed) {
				last = typed
				return
			}
			trimmed := strings.TrimSpace(typed)
			if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
				var nested any
				if json.Unmarshal([]byte(trimmed), &nested) == nil {
					visit(nested)
				}
			}
		case []any:
			for _, item := range typed {
				visit(item)
			}
		case map[string]any:
			for _, item := range typed {
				visit(item)
			}
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var event any
		if json.Unmarshal([]byte(line), &event) == nil {
			visit(event)
		}
	}
	return last
}

// grokFinalImageChunk reads the last completed image_chunk out of the reply's
// cardAttachment payloads (progress 100 + a relative assets.grok.com path).
func grokFinalImageChunk(body string) (imageURL, mimeType string) {
	type imageChunk struct {
		Progress any    `json:"progress"`
		ImageURL string `json:"imageUrl"`
		MimeType string `json:"mimeType"`
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var outer struct {
			Result *struct {
				Response *struct {
					CardAttachment *struct {
						JSONData string `json:"jsonData"`
					} `json:"cardAttachment"`
				} `json:"response"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(line), &outer) != nil || outer.Result == nil ||
			outer.Result.Response == nil || outer.Result.Response.CardAttachment == nil {
			continue
		}
		var inner struct {
			ImageChunk *imageChunk `json:"image_chunk"`
		}
		if json.Unmarshal([]byte(outer.Result.Response.CardAttachment.JSONData), &inner) != nil || inner.ImageChunk == nil {
			continue
		}
		var progress float64
		switch typed := inner.ImageChunk.Progress.(type) {
		case float64:
			progress = typed
		case string:
			parsed, err := strconv.ParseFloat(typed, 64)
			if err != nil {
				continue
			}
			progress = parsed
		default:
			continue
		}
		if progress < 100 || inner.ImageChunk.ImageURL == "" {
			continue
		}
		imageURL = inner.ImageChunk.ImageURL
		mimeType = inner.ImageChunk.MimeType
	}
	return imageURL, mimeType
}

// grokMediaHeaders is the slimmer header set media turns carry.
func grokMediaHeaders(cookieHeader, requestID string) [][2]string {
	return [][2]string{
		{"Content-Type", "application/json"},
		{"Accept", "text/event-stream"},
		{"User-Agent", grokWebUserAgent},
		{"Sec-Ch-Ua", `"Chromium";v="146", "Not(A:Brand";v="24"`},
		{"Sec-Ch-Ua-Mobile", "?0"},
		{"Sec-Ch-Ua-Platform", `"macOS"`},
		{"Origin", grokWebOrigin},
		{"Referer", grokWebOrigin + "/"},
		{"Browser-Id", requestID},
		{"x-xai-request-id", requestID},
		{"x-statsig-id", grokStatsigID()},
		{"Cookie", cookieHeader},
	}
}

// GenerateGrokMedia runs one Imagine turn. kind is "image" or "video"; the
// cookie is whatever the user pasted (see grokBuildCookieHeader). The client
// reference carries the connection's proxy assignment.
func GenerateGrokMedia(ctx context.Context, reference *http.Client, cookieRaw, prompt, kind, size string, duration float64, n int) ([]GrokMediaResult, error) {
	if kind != "image" && kind != "video" {
		return nil, fmt.Errorf("grok-web supports image and video generation only")
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("prompt is required for media generation")
	}
	cookieHeader := grokBuildCookieHeader(cookieRaw)
	if cookieHeader == "" {
		return nil, fmt.Errorf("grok-web requires the grok.com Cookie header (sso) pasted into the connection")
	}

	// Video quota is ~1/day even on paid tiers — never loop video requests.
	iterations := 1
	if kind == "image" {
		iterations = max(1, n)
	}

	doer, err := grokTransportFactory(reference, grokWebOrigin)
	if err != nil {
		return nil, err
	}

	results := make([]GrokMediaResult, 0, iterations)
	for i := 0; i < iterations; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		requestID := grokUUID()
		var payload grokMessagePayload
		if kind == "video" {
			payload = grokVideoPayload(prompt, size, duration)
		} else {
			payload = grokImagePayload(prompt, size)
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("grok-web: marshal %s payload: %w", kind, err)
		}

		callCtx, cancel := context.WithTimeout(ctx, grokRequestTimeout(kind))
		response, err := doer.do(callCtx, http.MethodPost, grokWebChatURL, grokMediaHeaders(cookieHeader, requestID), encoded)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("grok-web: %s request failed: %w", kind, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<20))
		_ = response.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("grok-web: read %s reply: %w", kind, readErr)
		}
		text := string(body)
		if response.StatusCode != http.StatusOK || grokIsCloudflareChallenge(text) {
			if grokIsCloudflareChallenge(text) {
				return nil, fmt.Errorf("grok-web returned a Cloudflare challenge — re-copy the full Cookie header (with cf_clearance) from a logged-in grok.com browser on this machine")
			}
			snippet := text
			if len(snippet) > 200 {
				snippet = snippet[:200]
			}
			return nil, fmt.Errorf("grok-web %s request failed: HTTP %d %s", kind, response.StatusCode, snippet)
		}

		if kind == "video" {
			videoURL := grokFindVideoURL(text)
			if videoURL == "" {
				tail := text
				if len(tail) > 300 {
					tail = tail[len(tail)-300:]
				}
				log.Warn("grok-web", "no video URL in stream", "tail", tail)
				return nil, fmt.Errorf("grok-web returned no video asset — quota may be exhausted (1 video/day) or the prompt was moderated")
			}
			if !strings.HasPrefix(videoURL, "http") {
				videoURL = grokAssetsBase + "/" + strings.TrimPrefix(videoURL, "/")
			}
			b64, mime, err := grokDownloadAsset(ctx, doer, videoURL, cookieHeader)
			if err != nil {
				return nil, fmt.Errorf("video download failed: %w", err)
			}
			results = append(results, GrokMediaResult{B64: b64, Mime: mime})
			break
		}

		imageURL, mimeType := grokFinalImageChunk(text)
		if imageURL == "" {
			return nil, fmt.Errorf("grok-web returned no completed image chunk — the prompt may have been moderated or image generation is unavailable for this account")
		}
		if !strings.HasPrefix(imageURL, "http") {
			imageURL = grokAssetsBase + "/" + strings.TrimPrefix(imageURL, "/")
		}
		if mimeType == "" {
			mimeType = "image/jpeg"
		}
		b64, mime, err := grokDownloadAsset(ctx, doer, imageURL, cookieHeader)
		if err != nil {
			return nil, fmt.Errorf("image download failed: %w", err)
		}
		if mime != "" {
			mimeType = mime
		}
		results = append(results, GrokMediaResult{B64: b64, Mime: mimeType})
	}
	log.Info("grok-web", "media generation completed", "kind", kind, "items", len(results))
	return results, nil
}

// grokDownloadAsset fetches one finished asset from assets.grok.com. The
// content type is taken from the response when it names a type.
func grokDownloadAsset(ctx context.Context, doer grokDoer, target, cookieHeader string) (string, string, error) {
	callCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	response, err := doer.do(callCtx, http.MethodGet, target, [][2]string{
		{"User-Agent", grokWebUserAgent},
		{"Referer", grokWebOrigin + "/"},
		{"Cookie", cookieHeader},
	}, nil)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HTTP %d", response.StatusCode)
	}
	buf, err := io.ReadAll(io.LimitReader(response.Body, 100<<20))
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(buf), response.Header.Get("Content-Type"), nil
}
