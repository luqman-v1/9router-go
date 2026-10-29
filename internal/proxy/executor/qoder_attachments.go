package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/proxy"
)

// Qoder attachment rewrite & OSS image upload — port of
// open-sse/shared/qoder/attachments.js.
//
// Native qodercli does NOT stuff image/PDF bytes into agent_chat_generation.
// It PUTs them to /algo/api/v2/image/upload (COSY-signed multipart) and then
// sends the returned OSS URL. Agents like Claude Code send OpenAI/Claude
// data-URIs instead, which upstream 9router previously forwarded verbatim —
// 10MB images become 30MB+ JSON and upstream 413s even though the model window
// is ~200k tokens.
//
// This module:
//   1. Uploads inlined images to Qoder's file API (cached by sha256).
//   2. Replaces huge non-image file blocks with a short stub.
//   3. Caps leftover data-URIs so the chat JSON stays small.

const (
	// qoderMaxImageBytes: images larger than this are stubbed without an upload attempt.
	// Matches upstream MAX_IMAGE_BYTES (10MB).
	qoderMaxImageBytes = 10 * 1024 * 1024

	// qoderMaxPayloadBytes: drop remaining inlined binaries if the Qoder JSON
	// body would still exceed this. Matches upstream QODER_MAX_PAYLOAD_BYTES (6 MiB).
	qoderMaxPayloadBytes = 6 * 1024 * 1024

	// qoderInlineFallbackMaxBytes: if OSS upload fails, keep tiny data-URIs;
	// anything larger is stubbed. Matches upstream QODER_INLINE_FALLBACK_MAX_BYTES (512 KiB).
	qoderInlineFallbackMaxBytes = 512 * 1024
)

var (
	qoderImageMimeRegex = regexp.MustCompile(`(?i)^image/`)
	qoderDataURIRegex   = regexp.MustCompile(`(?i)data:[^;]+;base64,[A-Za-z0-9+/=\s]+`)
)

func qoderMimeExt(mime string) string {
	m := strings.ToLower(mime)
	switch {
	case strings.Contains(m, "png"):
		return "png"
	case strings.Contains(m, "jpeg") || strings.Contains(m, "jpg"):
		return "jpg"
	case strings.Contains(m, "gif"):
		return "gif"
	case strings.Contains(m, "webp"):
		return "webp"
	case strings.Contains(m, "bmp"):
		return "bmp"
	case strings.Contains(m, "pdf"):
		return "pdf"
	default:
		return "bin"
	}
}

func qoderDecodedBytes(b64 string) int {
	if b64 == "" {
		return 0
	}
	compact := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, b64)
	return len(compact) * 3 / 4
}

func qoderStubText(name, mime string, sizeBytes int, reason string) string {
	label := name
	if label == "" {
		label = mime
	}
	if label == "" {
		label = "attachment"
	}
	size := ""
	if sizeBytes > 0 {
		size = fmt.Sprintf(", %d bytes", sizeBytes)
	}
	return fmt.Sprintf("[file omitted: %s%s — %s]", label, size, reason)
}

func qoderBuildMultipartFile(data []byte, fileName, mediaType string) (string, []byte) {
	boundary := fmt.Sprintf("----9routerQoder%x%s", time.Now().UnixNano(), uuid.NewString()[:8])
	if fileName == "" {
		fileName = "upload." + qoderMimeExt(mediaType)
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "--%s\r\nContent-Disposition: form-data; name=\"file\"; filename=\"%s\"\r\nContent-Type: %s\r\n\r\n",
		boundary, fileName, mediaType)
	buf.Write(data)
	fmt.Fprintf(&buf, "\r\n--%s--\r\n", boundary)

	return boundary, buf.Bytes()
}

func qoderExtractURLFromUploadResponse(body []byte) string {
	var val any
	if err := json.Unmarshal(body, &val); err != nil {
		return ""
	}
	root, ok := val.(map[string]any)
	if !ok {
		return ""
	}
	result := root
	if r, ok := root["result"].(map[string]any); ok {
		result = r
	}

	for _, arrCandidate := range []any{result["imageUrls"], result["image_urls"], root["imageUrls"], root["image_urls"]} {
		if arr, ok := arrCandidate.([]any); ok && len(arr) > 0 {
			if s, ok := arr[0].(string); ok && s != "" {
				return s
			}
		}
	}
	for _, key := range []string{"imageUrl", "image_url", "url", "ossUrl", "oss_url", "originalUrl", "originUrl", "link", "image"} {
		for _, candidate := range []map[string]any{result, root} {
			if v, ok := candidate[key].(string); ok && v != "" {
				return v
			}
		}
	}
	if innerBody, ok := root["body"].(string); ok && innerBody != "" {
		return qoderExtractURLFromUploadResponse([]byte(innerBody))
	}
	return ""
}

// qoderUploadFn is the signature for an image uploader, matching upstream defaultUploadImage.
type qoderUploadFn func(ctx context.Context, buffer []byte, mediaType string) (string, error)

func defaultQoderUploadImage(ctx context.Context, client *http.Client, buffer []byte, mediaType string, creds QoderCosyCreds, region QoderRegion) (string, error) {
	reqID := uuid.NewString()
	host := qoderInferenceHost(region, creds.AuthToken)
	uploadURL := fmt.Sprintf("https://%s/algo%s?request_id=%s", host, QoderImageUploadSigPath, reqID)

	fileName := "image." + qoderMimeExt(mediaType)
	boundary, body := qoderBuildMultipartFile(buffer, fileName, mediaType)

	headers, err := buildQoderCosyHeaders(body, uploadURL, creds)
	if err != nil {
		return "", fmt.Errorf("build upload COSY headers: %w", err)
	}
	headers["Accept"] = "application/json"
	headers["Content-Type"] = fmt.Sprintf("multipart/form-data; boundary=%s", boundary)
	headers["Content-Length"] = fmt.Sprintf("%d", len(body))
	headers["AI-CLIENT-TIMESTAMP"] = fmt.Sprintf("%d", time.Now().Unix())
	headers["Accept-Encoding"] = "identity"

	resp, err := proxy.DoRequest(ctx, client, http.MethodPut, uploadURL, headers, body)
	if err != nil {
		return "", fmt.Errorf("upload image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read upload response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, qoderTruncate(string(respBytes), 180))
	}

	uploadedURL := qoderExtractURLFromUploadResponse(respBytes)
	if uploadedURL == "" {
		return "", fmt.Errorf("upload response missing url")
	}
	return uploadedURL, nil
}

type qoderUploadCtx struct {
	ctx      context.Context
	uploadFn qoderUploadFn
	cache    map[string]string // sha256 -> url
}

type qoderUploadResult struct {
	url   string
	keep  bool
	stub  bool
	bytes int
	mime  string
}

func qoderUploadImageData(uctx *qoderUploadCtx, b64, mediaType string) *qoderUploadResult {
	compact := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, b64)
	if compact == "" {
		return nil
	}
	bytesCount := len(compact) * 3 / 4
	if bytesCount > qoderMaxImageBytes {
		return &qoderUploadResult{stub: true, bytes: bytesCount, mime: mediaType}
	}

	raw, err := base64.StdEncoding.DecodeString(compact)
	if err != nil {
		return &qoderUploadResult{stub: true, bytes: bytesCount, mime: mediaType}
	}

	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if uctx.cache != nil {
		if cached, ok := uctx.cache[digest]; ok {
			return &qoderUploadResult{url: cached, bytes: bytesCount, mime: mediaType}
		}
	}

	if uctx.uploadFn != nil {
		url, err := uctx.uploadFn(uctx.ctx, raw, mediaType)
		if err == nil && url != "" {
			if uctx.cache != nil {
				uctx.cache[digest] = url
			}
			return &qoderUploadResult{url: url, bytes: bytesCount, mime: mediaType}
		}
	}

	if bytesCount <= qoderInlineFallbackMaxBytes {
		return &qoderUploadResult{keep: true, bytes: bytesCount, mime: mediaType}
	}
	return &qoderUploadResult{stub: true, bytes: bytesCount, mime: mediaType}
}

// qoderParseDataURI parses `data:image/png;base64,...` returning mimeType and raw base64.
func qoderParseDataURI(uri string) (string, string, bool) {
	if !strings.HasPrefix(strings.ToLower(uri), "data:") {
		return "", "", false
	}
	rest := uri[5:]
	semi := strings.Index(rest, ";")
	if semi < 0 {
		return "", "", false
	}
	mime := rest[:semi]
	afterSemi := rest[semi+1:]
	if !strings.HasPrefix(strings.ToLower(afterSemi), "base64,") {
		return "", "", false
	}
	b64 := afterSemi[7:]
	return mime, b64, true
}

func qoderRewriteBlock(block map[string]any, uctx *qoderUploadCtx) any {
	bType, _ := block["type"].(string)

	if bType == qoderBlockImageURL {
		var raw string
		switch v := block["image_url"].(type) {
		case string:
			raw = v
		case map[string]any:
			raw, _ = v["url"].(string)
		}
		if raw == "" {
			return nil
		}
		if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
			return qoderImageBlock(raw)
		}
		mime, b64, ok := qoderParseDataURI(raw)
		if !ok {
			return map[string]any{
				"type": qoderBlockText,
				"text": qoderStubText("attachment", "", 0, "unreadable data URI"),
			}
		}
		if !qoderImageMimeRegex.MatchString(mime) {
			return map[string]any{
				"type": qoderBlockText,
				"text": qoderStubText("file", mime, qoderDecodedBytes(b64), "non-image bytes are not inlined into Qoder context"),
			}
		}
		up := qoderUploadImageData(uctx, b64, mime)
		if up != nil && up.url != "" {
			return qoderImageBlock(up.url)
		}
		if up != nil && up.keep {
			return qoderImageBlock(raw)
		}
		var sz int
		if up != nil {
			sz = up.bytes
		}
		return map[string]any{
			"type": qoderBlockText,
			"text": qoderStubText("image", mime, sz, "upload failed; not inlined"),
		}
	}

	if bType == qoderBlockImage {
		src, _ := block["source"].(map[string]any)
		srcType, _ := src["type"].(string)
		if srcType == "url" {
			if u, ok := src["url"].(string); ok && u != "" {
				return qoderImageBlock(u)
			}
		}
		if srcType == "base64" {
			data, _ := src["data"].(string)
			mime, _ := src["media_type"].(string)
			if mime == "" {
				mime = "image/png"
			}
			up := qoderUploadImageData(uctx, data, mime)
			if up != nil && up.url != "" {
				return qoderImageBlock(up.url)
			}
			if up != nil && up.keep {
				return qoderImageBlock(fmt.Sprintf("data:%s;base64,%s", mime, data))
			}
			var sz int
			if up != nil {
				sz = up.bytes
			}
			return map[string]any{
				"type": qoderBlockText,
				"text": qoderStubText("image", mime, sz, "upload failed; not inlined"),
			}
		}
	}

	if bType == qoderBlockFile {
		file, _ := block["file"].(map[string]any)
		name := qoderFileName(block)
		dataURI, _ := file["file_data"].(string)
		var b64, mime string
		if dataURI != "" {
			if m, b, ok := qoderParseDataURI(dataURI); ok {
				mime, b64 = m, b
			} else if !strings.HasPrefix(dataURI, "data:") {
				b64 = dataURI
			}
		}
		if fmtVal, ok := file["format"].(string); ok && mime == "" {
			mime = fmtVal
		}
		if mime == "" {
			mime = "application/octet-stream"
		}
		if b64 != "" && qoderImageMimeRegex.MatchString(mime) {
			up := qoderUploadImageData(uctx, b64, mime)
			if up != nil && up.url != "" {
				return qoderImageBlock(up.url)
			}
		}
		return map[string]any{
			"type": qoderBlockText,
			"text": qoderStubText(name, mime, qoderDecodedBytes(b64), "Qoder reads documents via its file API, not inlined bytes"),
		}
	}

	if bType == qoderBlockDocument {
		src, _ := block["source"].(map[string]any)
		name, _ := block["title"].(string)
		if name == "" {
			name = "document"
		}
		srcType, _ := src["type"].(string)
		data, _ := src["data"].(string)
		if srcType == "base64" && data != "" {
			mime, _ := src["media_type"].(string)
			if mime == "" {
				mime = "application/pdf"
			}
			if qoderImageMimeRegex.MatchString(mime) {
				up := qoderUploadImageData(uctx, data, mime)
				if up != nil && up.url != "" {
					return qoderImageBlock(up.url)
				}
			}
			return map[string]any{
				"type": qoderBlockText,
				"text": qoderStubText(name, mime, qoderDecodedBytes(data), "Qoder reads documents via its file API, not inlined bytes"),
			}
		}
	}

	if text, ok := block["text"].(string); ok && strings.Contains(text, "data:") && len(text) > 8192 {
		next := qoderDataURIRegex.ReplaceAllStringFunc(text, func(m string) string {
			mime, b64, ok := qoderParseDataURI(strings.TrimSpace(m))
			bytesCount := len(m)
			if ok {
				bytesCount = qoderDecodedBytes(b64)
			}
			if bytesCount <= qoderInlineFallbackMaxBytes {
				return m
			}
			return qoderStubText("", mime, bytesCount, "inlined data URI stripped from Qoder context")
		})
		blockCopy := make(map[string]any, len(block))
		for k, v := range block {
			blockCopy[k] = v
		}
		blockCopy["text"] = next
		return blockCopy
	}

	return block
}

func qoderRewriteContent(content any, uctx *qoderUploadCtx) any {
	switch v := content.(type) {
	case string:
		if strings.Contains(v, "data:") && len(v) > 8192 {
			return qoderDataURIRegex.ReplaceAllStringFunc(v, func(m string) string {
				mime, b64, ok := qoderParseDataURI(strings.TrimSpace(m))
				bytesCount := len(m)
				if ok {
					bytesCount = qoderDecodedBytes(b64)
				}
				if bytesCount <= qoderInlineFallbackMaxBytes {
					return m
				}
				return qoderStubText("", mime, bytesCount, "inlined data URI stripped from Qoder context")
			})
		}
		return v
	case []any:
		var out []any
		for _, item := range v {
			if block, ok := item.(map[string]any); ok {
				next := qoderRewriteBlock(block, uctx)
				if next != nil {
					out = append(out, next)
				}
			} else {
				out = append(out, item)
			}
		}
		if len(out) == 0 {
			return ""
		}
		return out
	default:
		return content
	}
}

func qoderPayloadBytes(messages []any) int {
	b, err := json.Marshal(messages)
	if err != nil {
		return 0
	}
	return len(b)
}

func qoderStripRemainingDataURIs(messages []any) {
	for _, item := range messages {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch c := msg["content"].(type) {
		case string:
			if strings.Contains(c, "data:") {
				msg["content"] = qoderDataURIRegex.ReplaceAllStringFunc(c, func(m string) string {
					return qoderStubText("", "", len(m), "payload over Qoder size budget")
				})
			}
		case []any:
			var next []any
			for _, b := range c {
				block, ok := b.(map[string]any)
				if !ok {
					next = append(next, b)
					continue
				}
				if block["type"] == qoderBlockImageURL {
					var raw string
					switch u := block["image_url"].(type) {
					case string:
						raw = u
					case map[string]any:
						raw, _ = u["url"].(string)
					}
					if strings.HasPrefix(raw, "data:") {
						next = append(next, map[string]any{
							"type": qoderBlockText,
							"text": qoderStubText("image", "", 0, "payload over Qoder size budget"),
						})
						continue
					}
				}
				if text, ok := block["text"].(string); ok && strings.Contains(text, "data:") {
					blockCopy := make(map[string]any, len(block))
					for k, v := range block {
						blockCopy[k] = v
					}
					blockCopy["text"] = qoderDataURIRegex.ReplaceAllStringFunc(text, func(m string) string {
						return qoderStubText("", "", len(m), "payload over Qoder size budget")
					})
					next = append(next, blockCopy)
					continue
				}
				next = append(next, block)
			}
			msg["content"] = next
		}
	}
}

// QoderAttachmentStats describes the outcome of rewriting message attachments.
type QoderAttachmentStats struct {
	ImageURLs []string
	Uploaded  int
	Stubbed   int
}

// RewriteQoderMessageAttachments rewrites OpenAI-shaped messages in place:
// uploads data-URI images to Qoder's OSS upload endpoint, stubs non-image files,
// and enforces payload budgets. Port of upstream rewriteQoderMessageAttachments.
func RewriteQoderMessageAttachments(ctx context.Context, messages []any, creds QoderCosyCreds, region QoderRegion, client *http.Client, uploadFn qoderUploadFn) (QoderAttachmentStats, error) {
	stats := QoderAttachmentStats{ImageURLs: []string{}}
	if len(messages) == 0 {
		return stats, nil
	}

	fn := uploadFn
	if fn == nil && client != nil {
		fn = func(ctx context.Context, buffer []byte, mediaType string) (string, error) {
			return defaultQoderUploadImage(ctx, client, buffer, mediaType, creds, region)
		}
	}

	uctx := &qoderUploadCtx{
		ctx:      ctx,
		uploadFn: fn,
		cache:    map[string]string{},
	}

	for _, item := range messages {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		// Ollama-style sidecar: fold into content so normalizeMessages sees them.
		if rawImages, ok := msg["images"].([]any); ok && len(rawImages) > 0 {
			var extras []any
			for _, img := range rawImages {
				if s, ok := img.(string); ok && s != "" {
					extras = append(extras, qoderImageBlock(s))
				}
			}
			switch c := msg["content"].(type) {
			case []any:
				msg["content"] = append(c, extras...)
			case string:
				msg["content"] = append([]any{map[string]any{"type": qoderBlockText, "text": c}}, extras...)
			default:
				msg["content"] = extras
			}
			delete(msg, "images")
		}

		msg["content"] = qoderRewriteContent(msg["content"], uctx)
	}

	// Collect surviving http(s) image URLs.
	for _, item := range messages {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, b := range blocks {
			block, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if block["type"] == qoderBlockImageURL {
				var raw string
				switch u := block["image_url"].(type) {
				case string:
					raw = u
				case map[string]any:
					raw, _ = u["url"].(string)
				}
				if strings.HasPrefix(strings.ToLower(raw), "http://") || strings.HasPrefix(strings.ToLower(raw), "https://") {
					stats.ImageURLs = append(stats.ImageURLs, raw)
				}
			}
			if block["type"] == qoderBlockText {
				if text, ok := block["text"].(string); ok && strings.HasPrefix(text, "[file omitted:") {
					stats.Stubbed++
				}
			}
		}
	}
	stats.Uploaded = len(stats.ImageURLs)

	if qoderPayloadBytes(messages) > qoderMaxPayloadBytes {
		qoderStripRemainingDataURIs(messages)
	}

	return stats, nil
}
