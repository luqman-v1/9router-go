package executor

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/proxy"
)

// Image input on Gemini Web.
//
// The site accepts an image as an *attachment*, not as text: the bytes go to
// Google's content-push service first and the path it returns is placed in
// the turn's slot 0. The same path is what the site's own composer sends, so
// there is no alternative shape to try.
//
// The upload is a separate request from the turn, so it is a separate failure:
// an image that cannot be uploaded has to be reported before the turn is sent,
// not as an empty answer afterwards.
//
// Reference: azrinaldi/gemini-stealth-bridge internal/gemini/stream.go
// (uploadFileLocked) — the endpoint, the multipart envelope and the
// resumable fallback are that project's shape, verified live against
// gemini.google.com on 2026-10-20.

// geminiWebUploadEndpoint is the content-push service the site's own composer
// uploads to. The fallback lives on a different host because Google retires
// the primary path periodically.
const (
	geminiWebUploadEndpoint    = "https://content-push.googleapis.com/upload"
	geminiWebUploadFallback    = "https://push.clients6.google.com/upload/"
	geminiWebUploadTenant      = "bard-storage"
	geminiWebUploadDefaultPush = "feeds/mcudyrk2a4khkz"
)

// geminiWebUploadPath is where the content-push service is reached when the
// origin is not the public one — a mirror, or a test's own server.
const geminiWebUploadPath = "/upload"

// geminiWebUploadFallbackPath is where the resumable upload service is
// reached when the origin is not the public one.
const geminiWebUploadFallbackPath = "/upload/"

// geminiWebMaxImageBytes bounds one image.
//
// The site accepts far larger uploads, but a request that carries one is
// already unusual, and an unbounded read would let a client make the router
// hold an arbitrary amount of memory. 20 MB is the same ceiling the reference
// bridge puts on a fetched remote image.
const geminiWebMaxImageBytes = 20 << 20

// geminiWebAttachment is one uploaded image, in the shape slot 0 expects.
type geminiWebAttachment struct {
	// Path is what content-push returned: an opaque path such as
	// "/contrib_service/ttl_1d/…". The site resolves it server-side.
	Path string
	// Name is the filename the site shows for the attachment.
	Name string
}

// slot renders the attachment as the tuple the turn's slot 0 carries.
func (a geminiWebAttachment) slot() []any {
	return []any{[]string{a.Path}, a.Name}
}

// geminiWebImage is one image a turn wants the model to look at.
type geminiWebImage struct {
	// Data is the decoded bytes.
	Data []byte
	// MIME is the content type the upload announces.
	MIME string
	// Name is the filename the upload announces.
	Name string
}

// geminiWebImageSource is one image reference, before it is resolved.
type geminiWebImageSource struct {
	// URL is the location of the image: a data URL or an http(s) URL.
	URL string
	// Name is the filename to announce, when the client gave one.
	Name string
}

// geminiWebCollectImages reads every image out of the conversation.
//
// Three spellings reach the executor, because three clients send this shape:
// OpenAI's `image_url` part (a string or a {url} object), Claude's
// `{type:"image",source:{…}}` block (base64 or a URL), and the Responses API's
// `input_image` part. They are folded into one shape here so the upload path
// has a single input.
//
// A data URL is decoded locally; a remote URL is fetched, because a client
// that sends a link expects the image, not the link.
func geminiWebCollectImages(ctx context.Context, messages []deepSeekMessage) ([]geminiWebImage, error) {
	var images []geminiWebImage
	for _, message := range messages {
		for _, source := range geminiWebImageSources(message.Content) {
			image, err := geminiWebResolveImage(ctx, source)
			if err != nil {
				return nil, err
			}
			if image != nil {
				images = append(images, *image)
			}
		}
	}
	return images, nil
}

// geminiWebImageSources reads every image reference out of one message's
// content.
func geminiWebImageSources(content jsontext.Value) []geminiWebImageSource {
	if len(content) == 0 {
		return nil
	}
	var asString string
	if err := json.Unmarshal(content, &asString); err == nil {
		return nil
	}
	var blocks []map[string]any
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil
	}
	var sources []geminiWebImageSource
	for _, block := range blocks {
		if source := geminiWebImageSourceOf(block); source != nil {
			sources = append(sources, *source)
		}
	}
	return sources
}

// geminiWebImageSourceOf reads one image block in any of the three spellings.
func geminiWebImageSourceOf(block map[string]any) *geminiWebImageSource {
	typ, _ := block["type"].(string)
	switch typ {
	case "image_url":
		url := geminiWebStringOf(block["image_url"])
		if url == "" {
			if object, ok := block["image_url"].(map[string]any); ok {
				url = geminiWebStringOf(object["url"])
			}
		}
		if url == "" {
			return nil
		}
		return &geminiWebImageSource{URL: url}
	case "input_image":
		url := geminiWebStringOf(block["image_url"])
		if url == "" {
			url = geminiWebStringOf(block["url"])
			if url == "" {
				url = geminiWebStringOf(block["file_url"])
			}
		}
		if url == "" {
			return nil
		}
		return &geminiWebImageSource{URL: url}
	case "image":
		source, ok := block["source"].(map[string]any)
		if !ok {
			return nil
		}
		data := geminiWebStringOf(source["data"])
		if data != "" {
			mime := geminiWebStringOf(source["media_type"])
			if mime == "" {
				mime = "image/png"
			}
			return &geminiWebImageSource{URL: "data:" + mime + ";base64," + data}
		}
		url := geminiWebStringOf(source["url"])
		if url == "" {
			return nil
		}
		return &geminiWebImageSource{URL: url}
	}
	return nil
}

// geminiWebStringOf reads a string field, ignoring anything else.
func geminiWebStringOf(value any) string {
	text, _ := value.(string)
	return text
}

// geminiWebResolveImage turns one reference into bytes.
//
// A remote fetch is bounded in size and restricted to http(s), so a client
// cannot make the router read a file:// path or an unbounded body.
// geminiWebResolveImage turns one reference into bytes.
//
// A remote fetch is bounded in size, restricted to http(s), and dialled
// through the repository's SSRF-guarded transport, so a client cannot make
// the router read a file:// path, reach a private address, or pull an
// unbounded body.
func geminiWebResolveImage(ctx context.Context, source geminiWebImageSource) (*geminiWebImage, error) {
	raw := strings.TrimSpace(source.URL)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "data:") {
		return geminiWebDecodeDataURL(raw, source.Name)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return nil, geminiWebFailure(http.StatusBadRequest,
			"gemini-web: the image URL could not be read")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, geminiWebFailure(http.StatusBadRequest,
			"gemini-web: only http and https image URLs are supported")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, geminiWebFailure(http.StatusBadRequest,
			"gemini-web: the image URL could not be read: "+err.Error())
	}
	request.Header.Set("User-Agent", geminiWebRemoteImageUserAgent)
	request.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	response, err := geminiWebImageClient().Do(request)
	if err != nil {
		return nil, geminiWebFailure(http.StatusBadGateway,
			"gemini-web: the image could not be fetched: "+err.Error())
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, geminiWebFailure(http.StatusBadGateway,
			fmt.Sprintf("gemini-web: the image URL answered HTTP %d", response.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, geminiWebMaxImageBytes+1))
	if err != nil {
		return nil, geminiWebFailure(http.StatusBadGateway,
			"gemini-web: the image could not be read: "+err.Error())
	}
	if len(data) > geminiWebMaxImageBytes {
		return nil, geminiWebFailure(http.StatusRequestEntityTooLarge,
			fmt.Sprintf("gemini-web: an image larger than %d bytes is not accepted", geminiWebMaxImageBytes))
	}
	mime := response.Header.Get("Content-Type")
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	return &geminiWebImage{
		Data: data,
		MIME: mime,
		Name: geminiWebImageName(source.Name, mime),
	}, nil
}

// geminiWebRemoteImageUserAgent is the browser identity the fetch presents.
const geminiWebRemoteImageUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"

// geminiWebImageClient is the client a remote image is fetched with.
//
// It is a package-level singleton on purpose: the transport pools its
// connections, and one image per turn would otherwise open a connection
// pool per turn.
var geminiWebImageClient = sync.OnceValue(func() *http.Client {
	return &http.Client{
		Timeout:   20 * time.Second,
		Transport: proxy.NewSafeTransport(),
	}
})

// geminiWebDecodeDataURL decodes a base64 data URL.
func geminiWebDecodeDataURL(raw, name string) (*geminiWebImage, error) {
	header, payload, found := strings.Cut(raw, ",")
	if !found {
		return nil, geminiWebFailure(http.StatusBadRequest,
			"gemini-web: the inline image is not a valid data URL")
	}
	mime := "image/png"
	if strings.HasPrefix(header, "data:") {
		if declared := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64"); declared != "" {
			mime = declared
		}
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, geminiWebFailure(http.StatusBadRequest,
			"gemini-web: the inline image is not valid base64")
	}
	if len(data) > geminiWebMaxImageBytes {
		return nil, geminiWebFailure(http.StatusRequestEntityTooLarge,
			fmt.Sprintf("gemini-web: an image larger than %d bytes is not accepted", geminiWebMaxImageBytes))
	}
	return &geminiWebImage{Data: data, MIME: mime, Name: geminiWebImageName(name, mime)}, nil
}

// geminiWebImageName names the uploaded file.
//
// The site's own composer names the file after its type, and the name is what
// the model sees when it refers to the attachment. A client that gave no name
// gets one derived from the mime type, so the model is never told about a
// file called "image".
func geminiWebImageName(name, mime string) string {
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return sanitizedGeminiWebFileName(trimmed)
	}
	extension := ".png"
	switch {
	case strings.Contains(mime, "jpeg"), strings.Contains(mime, "jpg"):
		extension = ".jpg"
	case strings.Contains(mime, "webp"):
		extension = ".webp"
	case strings.Contains(mime, "gif"):
		extension = ".gif"
	}
	return "image" + extension
}

// sanitizedGeminiWebFileName strips the characters that would break the
// multipart envelope.
func sanitizedGeminiWebFileName(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r == '\r' || r == '\n' {
			return '_'
		}
		return r
	}, name)
}

// geminiWebUploadImage uploads one image and returns the path the site
// resolves it by.
//
// Two paths are tried, in the order the site itself uses them: the multipart
// upload to content-push, and the resumable upload to push.clients6. The
// second exists because the first is retired periodically, and an image the
// router cannot upload is an image the model never sees.
func (s *geminiWebSession) uploadImage(ctx context.Context, image geminiWebImage) (geminiWebAttachment, error) {
	attachment, err := s.uploadMultipart(ctx, image)
	if err == nil {
		return attachment, nil
	}
	primaryErr := err
	attachment, fallbackErr := s.uploadResumable(ctx, image)
	if fallbackErr == nil {
		return attachment, nil
	}
	return geminiWebAttachment{}, geminiWebFailure(http.StatusBadGateway, fmt.Sprintf(
		"gemini-web: the image could not be uploaded (%s; the fallback upload failed too: %s)",
		primaryErr.Error(), fallbackErr.Error()))
}

// uploadMultipart is the primary path: a multipart form post to
// content-push whose answer is the path.
func (s *geminiWebSession) uploadMultipart(ctx context.Context, image geminiWebImage) (geminiWebAttachment, error) {
	boundary := "----GeminiWebUpload" + geminiWebRequestUUID()
	body := geminiWebMultipartBody(boundary, image)
	headers := map[string]string{
		"content-type":   "multipart/form-data; boundary=" + boundary,
		"origin":         s.endpoints.origin,
		"referer":        s.endpoints.origin + "/",
		"sec-fetch-site": "cross-site",
		"x-tenant-id":    geminiWebUploadTenant,
		"push-id":        s.pushID(),
	}
	response, err := s.do(ctx, http.MethodPost, s.endpoints.upload, strings.NewReader(body), headers["content-type"], headers)
	if err != nil {
		return geminiWebAttachment{}, err
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return geminiWebAttachment{}, fmt.Errorf("gemini-web: read the upload answer: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return geminiWebAttachment{}, fmt.Errorf("gemini-web: the upload answered HTTP %d", response.StatusCode)
	}
	path := strings.TrimSpace(string(payload))
	if !strings.HasPrefix(path, "/") {
		return geminiWebAttachment{}, fmt.Errorf("gemini-web: the upload answer carried no path")
	}
	return geminiWebAttachment{Path: path, Name: image.Name}, nil
}

// geminiWebUploadAttachments uploads every image the conversation names.
//
// The upload is per image and per session: each one is a separate request to
// content-push, and each one produces the path the turn's slot 0 carries.
func geminiWebUploadAttachments(ctx context.Context, session *geminiWebSession, messages []deepSeekMessage) ([]geminiWebAttachment, error) {
	images, err := geminiWebCollectImages(ctx, messages)
	if err != nil {
		return nil, err
	}
	if len(images) == 0 {
		return nil, nil
	}
	attachments := make([]geminiWebAttachment, 0, len(images))
	for _, image := range images {
		attachment, err := session.uploadImage(ctx, image)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, nil
}

// uploadResumable is the fallback path: a two-step Scotrup resumable upload.
func (s *geminiWebSession) uploadResumable(ctx context.Context, image geminiWebImage) (geminiWebAttachment, error) {
	initHeaders := map[string]string{
		"content-type":                        "application/x-www-form-urlencoded;charset=UTF-8",
		"origin":                              s.endpoints.origin,
		"referer":                             s.endpoints.origin + "/",
		"sec-fetch-site":                      "same-site",
		"x-tenant-id":                         geminiWebUploadTenant,
		"push-id":                             s.pushID(),
		"x-goog-upload-command":               "start",
		"x-goog-upload-protocol":              "resumable",
		"x-goog-upload-header-content-length": fmt.Sprintf("%d", len(image.Data)),
	}
	response, err := s.do(ctx, http.MethodPost, s.endpoints.uploadFallback,
		strings.NewReader("File name: "+image.Name), initHeaders["content-type"], initHeaders)
	if err != nil {
		return geminiWebAttachment{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return geminiWebAttachment{}, fmt.Errorf("gemini-web: the resumable upload answered HTTP %d", response.StatusCode)
	}
	uploadURL := response.Header.Get("x-goog-upload-url")
	if uploadURL == "" {
		return geminiWebAttachment{}, fmt.Errorf("gemini-web: the resumable upload returned no target URL")
	}
	putHeaders := map[string]string{
		"content-type":          "application/x-www-form-urlencoded;charset=UTF-8",
		"origin":                s.endpoints.origin,
		"referer":               s.endpoints.origin + "/",
		"sec-fetch-site":        "same-site",
		"x-tenant-id":           geminiWebUploadTenant,
		"push-id":               s.pushID(),
		"x-goog-upload-command": "upload, finalize",
		"x-goog-upload-offset":  "0",
	}
	putResponse, err := s.do(ctx, http.MethodPost, uploadURL,
		strings.NewReader(string(image.Data)), putHeaders["content-type"], putHeaders)
	if err != nil {
		return geminiWebAttachment{}, err
	}
	defer func() { _ = putResponse.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(putResponse.Body, 1<<20))
	if err != nil {
		return geminiWebAttachment{}, fmt.Errorf("gemini-web: read the upload answer: %w", err)
	}
	if putResponse.StatusCode != http.StatusOK {
		return geminiWebAttachment{}, fmt.Errorf("gemini-web: the resumable upload answered HTTP %d", putResponse.StatusCode)
	}
	path := strings.TrimSpace(string(payload))
	if !strings.HasPrefix(path, "/") {
		return geminiWebAttachment{}, fmt.Errorf("gemini-web: the upload answer carried no path")
	}
	return geminiWebAttachment{Path: path, Name: image.Name}, nil
}

// geminiWebMultipartBody renders the multipart envelope the primary upload
// posts.
func geminiWebMultipartBody(boundary string, image geminiWebImage) string {
	var b strings.Builder
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Disposition: form-data; name=\"file\"; filename=\"" + image.Name + "\"\r\n")
	b.WriteString("Content-Type: " + image.MIME + "\r\n\r\n")
	b.Write(image.Data)
	b.WriteString("\r\n--" + boundary + "--\r\n")
	return b.String()
}

// pushID is the feed the upload is announced under.
//
// The page carries one, and the site uses it to tie the upload to the
// session. A page that does not carry one gets the site's own default, which
// is what the reference bridge falls back to as well.
func (s *geminiWebSession) pushID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.page.pushID != "" {
		return s.page.pushID
	}
	return geminiWebUploadDefaultPush
}
