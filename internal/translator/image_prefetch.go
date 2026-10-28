package translator

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/netguard"
)

// Safe image prefetch (G11). Remote image URLs in an inbound request are
// downloaded here and rewritten to inline base64 before the request leaves
// the gateway, because several downstream formats (Claude Messages native,
// /v1/responses, kiro) accept only inline image bytes.
//
// Threat model and the exact rules live in image_prefetch_discovery.md:
// https-only, private/link-local/loopback refused at the literal-IP check and
// again at dial time (DNS rebinding), redirects re-validated, a hard byte cap
// and a MIME allowlist on both the header and the magic bytes.
//
// Fail-open: every refusal returns the original body untouched. No caller of
// this file should ever see an error escape.

// HTTP status codes used in Image Prefetch error classification.
const (
	StatusPrefetchBadRequest          = 400 // Bad URL scheme, invalid MIME, SSRF blocked, payload exceeded
	StatusPrefetchBadGateway          = 502 // Egress network error, connection refused, DNS lookup failure
	StatusPrefetchGatewayTimeout      = 504 // Context deadline exceeded, connection/read timeout
	StatusPrefetchClientClosedRequest = 499 // Downstream client canceled context (G03 lifecycle)
)

var (
	ErrPrefetchInvalidScheme   = errors.New("invalid image URL scheme: only http and https are permitted")
	ErrPrefetchSSRFBlocked     = errors.New("request to private/reserved IP blocked by SSRF protection")
	ErrPrefetchUnsupportedMIME = errors.New("unsupported MIME type: expected image/* format")
	ErrPrefetchPayloadExceeded = errors.New("image payload exceeds maximum allowed size for base64 inline conversion")
	ErrPrefetchNetworkError    = errors.New("egress network error connecting to remote image host")
	ErrPrefetchTimeout         = errors.New("gateway timeout while downloading remote image")
	ErrPrefetchClientCanceled  = errors.New("client closed request: context canceled during image prefetch")
)

// prefetchAllowedMIME is the allowlist from the discovery doc. SVG is
// deliberately absent: it can carry script and re-enters the client as
// markup, and so does every non-image type.
var prefetchAllowedMIME = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
	"image/gif":  true,
}

// EstimateBase64PayloadSize calculates the exact base64 encoded byte length for raw binary data.
// In standard base64 encoding, every 3 bytes produces 4 characters (with '=' padding).
func EstimateBase64PayloadSize(rawLen int64) int64 {
	if rawLen <= 0 {
		return 0
	}
	return ((rawLen + 2) / 3) * 4
}

// MaxRawSizeForBase64Limit calculates the maximum raw binary payload size that can fit
// within the downstream provider's payload limit after base64 expansion (~33.33% overhead).
func MaxRawSizeForBase64Limit(limitBytes int64) int64 {
	if limitBytes <= 0 {
		return 0
	}
	return (limitBytes / 4) * 3
}

// ValidatePrefetchURL validates the URL scheme and checks for direct SSRF host attempts.
func ValidatePrefetchURL(rawURL string) (int, error) {
	if rawURL == "" {
		return StatusPrefetchBadRequest, ErrPrefetchInvalidScheme
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return StatusPrefetchBadRequest, ErrPrefetchInvalidScheme
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return StatusPrefetchBadRequest, ErrPrefetchInvalidScheme
	}

	hostname := strings.ToLower(u.Hostname())
	if hostname == "" {
		return StatusPrefetchBadRequest, ErrPrefetchInvalidScheme
	}

	// Immediate static check for common loopback and metadata hostnames.
	if hostname == "localhost" || hostname == "metadata.google.internal" {
		return StatusPrefetchBadRequest, ErrPrefetchSSRFBlocked
	}

	// Check if hostname is a literal IP address. netguard.ParseIP strips an
	// IPv6 zone first: "::1%lo" names the same destination as "::1", but
	// net.ParseIP answers nil for it and the check below would be skipped.
	ip := netguard.ParseIP(hostname)
	if ip != nil && netguard.IsPrivateOrReserved(ip) {
		return StatusPrefetchBadRequest, ErrPrefetchSSRFBlocked
	}

	return http.StatusOK, nil
}

// ValidatePrefetchMIME ensures the Content-Type is a valid supported image MIME type.
func ValidatePrefetchMIME(contentType string) (int, error) {
	cleanType := strings.ToLower(strings.TrimSpace(contentType))
	if idx := strings.Index(cleanType, ";"); idx != -1 {
		cleanType = strings.TrimSpace(cleanType[:idx])
	}

	if !strings.HasPrefix(cleanType, "image/") {
		return StatusPrefetchBadRequest, ErrPrefetchUnsupportedMIME
	}

	if !prefetchAllowedMIME[cleanType] {
		return StatusPrefetchBadRequest, ErrPrefetchUnsupportedMIME
	}
	return http.StatusOK, nil
}

// ClassifyPrefetchError maps errors encountered during the image prefetch lifecycle
// to standard HTTP status codes and contract error categories.
func ClassifyPrefetchError(err error) (int, string, error) {
	if err == nil {
		return http.StatusOK, "success", nil
	}

	// 1. Downstream cancellation (Client Closed Request - G03 compliance)
	if errors.Is(err, context.Canceled) {
		return StatusPrefetchClientClosedRequest, "downstream_canceled", ErrPrefetchClientCanceled
	}

	// 2. Gateway Timeout (Context deadline or net timeout)
	if errors.Is(err, context.DeadlineExceeded) {
		return StatusPrefetchGatewayTimeout, "gateway_timeout", ErrPrefetchTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return StatusPrefetchGatewayTimeout, "gateway_timeout", ErrPrefetchTimeout
	}

	// 3. Bad Request: SSRF blocked
	if errors.Is(err, ErrPrefetchSSRFBlocked) || strings.Contains(strings.ToLower(err.Error()), "ssrf") ||
		strings.Contains(err.Error(), "private/reserved IP") {
		return StatusPrefetchBadRequest, "ssrf_blocked", ErrPrefetchSSRFBlocked
	}

	// 4. Bad Request: Invalid Scheme
	if errors.Is(err, ErrPrefetchInvalidScheme) {
		return StatusPrefetchBadRequest, "invalid_scheme", ErrPrefetchInvalidScheme
	}

	// 5. Bad Request: Unsupported MIME
	if errors.Is(err, ErrPrefetchUnsupportedMIME) {
		return StatusPrefetchBadRequest, "unsupported_mime", ErrPrefetchUnsupportedMIME
	}

	// 6. Bad Request: Payload Limit Exceeded
	if errors.Is(err, ErrPrefetchPayloadExceeded) {
		return StatusPrefetchBadRequest, "payload_exceeded", ErrPrefetchPayloadExceeded
	}

	// 7. Bad Gateway: Egress Network Errors
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return StatusPrefetchBadGateway, "egress_network_error", ErrPrefetchNetworkError
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return StatusPrefetchBadGateway, "egress_network_error", ErrPrefetchNetworkError
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return StatusPrefetchBadGateway, "egress_network_error", ErrPrefetchNetworkError
	}

	// Fallback for generic remote egress failure
	return StatusPrefetchBadGateway, "egress_network_error", ErrPrefetchNetworkError
}

// PrefetchOptions bounds one prefetch pass. Zero fields fall back to the
// defaults, so the caller can pass PrefetchOptions{} and get safe behaviour.
type PrefetchOptions struct {
	// MaxImages caps how many images a single request may pull. The rest are
	// left as remote URLs for the provider to deal with (or reject).
	MaxImages int
	// MaxBytes caps one image's raw (pre-base64) size.
	MaxBytes int64
	// Timeout bounds the whole prefetch pass, on top of the caller's context.
	Timeout time.Duration
	// AllowLoopback permits loopback hosts (a local, trusted image host). The
	// URL must still be explicitly loopback; private ranges stay blocked.
	AllowLoopback bool
	// Client overrides the HTTP client. Tests use it; production leaves it nil.
	Client *http.Client
}

func defaultPrefetchOptions() PrefetchOptions {
	return PrefetchOptions{
		MaxImages: 4,
		MaxBytes:  MaxRawSizeForBase64Limit(20 << 20), // 15MB raw -> 20MB base64
		Timeout:   15 * time.Second,
	}
}

func (o PrefetchOptions) normalized() PrefetchOptions {
	d := defaultPrefetchOptions()
	if o.MaxImages <= 0 {
		o.MaxImages = d.MaxImages
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = d.MaxBytes
	}
	if o.Timeout <= 0 {
		o.Timeout = d.Timeout
	}
	return o
}

// PrefetchInlineImages is the translator-layer entry point: it rewrites every
// remote image URL in an OpenAI- or Claude-shaped request body into inline
// base64, and returns body unchanged on any failure.
//
// The production caller is prefetchImagesForTarget in internal/handlers/chat,
// called from tryForwardWithConnection once the target provider is known:
// only providers that cannot fetch a URL themselves get the rewrite.
func PrefetchInlineImages(ctx context.Context, body []byte, opts PrefetchOptions) []byte {
	if ctx == nil {
		ctx = context.Background()
	}
	opts = opts.normalized()

	// The top level is decoded into raw values, not map[string]any: a plain
	// round-trip reads every number as float64, and a request carrying
	// `"seed": 9007199254740993` came back as …992. Sibling fields the rewrite
	// never touches have to survive it byte for byte.
	var raw map[string]jsontext.Value
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return body
	}
	// Chat Completions carries the turn under `messages`; the Responses wire
	// (which this gateway also serves) carries it under `input`. Missing both
	// is not an error — there is simply nothing to rewrite.
	key := "messages"
	if _, ok := raw[key]; !ok {
		key = "input"
	}
	itemsRaw, ok := raw[key]
	if !ok || len(itemsRaw) == 0 {
		return body
	}
	var items []map[string]any
	if err := json.Unmarshal(itemsRaw, &items); err != nil || len(items) == 0 {
		return body
	}

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	client := opts.Client
	if client == nil {
		client = newPrefetchClient(opts.AllowLoopback)
	}

	budget := opts.MaxImages
	rewritten := false
	for _, item := range items {
		changed := false
		walkImageBlocks(ctx, client, opts, item, &budget, &changed)
		if changed {
			rewritten = true
		}
	}
	if !rewritten {
		return body
	}

	encoded, err := json.Marshal(items, json.Deterministic(true))
	if err != nil {
		return body
	}
	raw[key] = jsontext.Value(encoded)
	out, err := json.Marshal(raw, json.Deterministic(true))
	if err != nil {
		return body
	}
	return out
}

// walkImageBlocks rewrites image blocks inside node["content"] (and nested
// tool_result content arrays) in both wire shapes.
func walkImageBlocks(ctx context.Context, client *http.Client, opts PrefetchOptions, node map[string]any, budget *int, changed *bool) {
	content, ok := node["content"]
	if !ok {
		return
	}
	blocks, ok := content.([]any)
	if !ok {
		return
	}
	for _, bRaw := range blocks {
		block, ok := bRaw.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "image_url":
			rewriteOpenAIImage(ctx, client, opts, block, budget, changed)
		case "input_image":
			// Responses wire: the URL sits directly on the block, not nested
			// under image_url, and file_id references are not URLs to fetch.
			rewriteResponsesImage(ctx, client, opts, block, budget, changed)
		case "image":
			rewriteClaudeImage(ctx, client, opts, block, budget, changed)
		case "tool_result":
			// Claude tool results can carry their own content array.
			walkImageBlocks(ctx, client, opts, block, budget, changed)
		}
	}
}

// rewriteOpenAIImage handles both shapes OpenAI accepts for `image_url`: the
// object form ({"url": "…"}) and the bare string. The string form is short
// enough to look like a typo and is the shape most clients emit, so skipping it
// left the remote URL in place for exactly the requests that would have used
// it.
func rewriteOpenAIImage(ctx context.Context, client *http.Client, opts PrefetchOptions, block map[string]any, budget *int, changed *bool) {
	switch iu := block["image_url"].(type) {
	case string:
		if !isRemoteImageURL(iu) {
			return
		}
		if dataURL, ok := prefetchOne(ctx, client, opts, iu, budget); ok {
			block["image_url"] = dataURL
			*changed = true
		}
	case map[string]any:
		raw, ok := iu["url"].(string)
		if !ok || !isRemoteImageURL(raw) {
			return
		}
		if dataURL, ok := prefetchOne(ctx, client, opts, raw, budget); ok {
			iu["url"] = dataURL
			*changed = true
		}
	}
}

func rewriteResponsesImage(ctx context.Context, client *http.Client, opts PrefetchOptions, block map[string]any, budget *int, changed *bool) {
	raw, ok := block["image_url"].(string)
	if !ok || !isRemoteImageURL(raw) {
		return
	}
	dataURL, ok := prefetchOne(ctx, client, opts, raw, budget)
	if !ok {
		return
	}
	block["image_url"] = dataURL
	*changed = true
}

func rewriteClaudeImage(ctx context.Context, client *http.Client, opts PrefetchOptions, block map[string]any, budget *int, changed *bool) {
	src, ok := block["source"].(map[string]any)
	if !ok {
		return
	}
	srcType, _ := src["type"].(string)
	if srcType != "url" {
		return
	}
	raw, ok := src["url"].(string)
	if !ok || !isRemoteImageURL(raw) {
		return
	}
	dataURL, ok := prefetchOne(ctx, client, opts, raw, budget)
	if !ok {
		return
	}
	media, b64, found := strings.Cut(strings.TrimPrefix(dataURL, "data:"), ";base64,")
	if !found {
		return
	}
	block["source"] = map[string]any{
		"type":       "base64",
		"media_type": media,
		"data":       b64,
	}
	*changed = true
}

func isRemoteImageURL(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// prefetchOne downloads one image and returns "data:<mime>;base64,<data>".
// It never returns an error: a refusal is logged and reported as ok=false so
// the caller leaves the block untouched.
func prefetchOne(ctx context.Context, client *http.Client, opts PrefetchOptions, raw string, budget *int) (string, bool) {
	if *budget <= 0 {
		return "", false
	}

	if err := ctx.Err(); err != nil {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	loopback := opts.AllowLoopback && isLiteralLoopbackHost(u.Hostname())
	if !loopback {
		if status, verr := ValidatePrefetchURL(raw); verr != nil {
			log.Warn("translator", "image prefetch refused", "status", status, "error", verr)
			return "", false
		}
		// https-only unless the host is explicitly loopback.
		if !strings.EqualFold(u.Scheme, "https") {
			return "", false
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", false
	}
	resp, err := client.Do(req)
	if err != nil {
		status, _, _ := ClassifyPrefetchError(err)
		log.Warn("translator", "image prefetch failed", "status", status, "error", err)
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", false
	}
	if resp.ContentLength > opts.MaxBytes {
		log.Warn("translator", "image prefetch oversized", "content_length", resp.ContentLength)
		return "", false
	}

	head := make([]byte, 512)
	n, err := io.ReadFull(resp.Body, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", false
	}
	head = head[:n]

	mimeType, err := prefetchMIMEType(resp.Header.Get("Content-Type"), head)
	if err != nil {
		return "", false
	}

	rest, err := io.ReadAll(io.LimitReader(resp.Body, opts.MaxBytes-int64(n)+1))
	if err != nil {
		return "", false
	}
	total := int64(n) + int64(len(rest))
	if total > opts.MaxBytes {
		log.Warn("translator", "image prefetch oversized", "bytes", total)
		return "", false
	}
	full := make([]byte, 0, total)
	full = append(full, head...)
	full = append(full, rest...)

	// Charge the budget only for an image that was actually inlined. Counting
	// refusals let four rejected URLs — a private address, a bad scheme, an
	// oversized body, a wrong MIME — exhaust the allowance and silently skip
	// every legitimate image after them in the same request.
	*budget--

	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(full), true
}

// prefetchMIMEType decides the media type the inlined bytes are labelled with.
//
// The magic bytes win, not the declared Content-Type. Labelling GIF bytes as
// image/png because the server said so does not smuggle anything past this
// gateway, but it does hand the provider a lie — and a provider that trusts the
// label will decode the wrong frames. A declared type outside the allowlist is
// still refused, so a server cannot downgrade an image to text/html.
func prefetchMIMEType(declared string, head []byte) (string, error) {
	cleanType := strings.ToLower(strings.TrimSpace(declared))
	if idx := strings.Index(cleanType, ";"); idx != -1 {
		cleanType = strings.TrimSpace(cleanType[:idx])
	}
	if _, err := ValidatePrefetchMIME(cleanType); err != nil {
		return "", err
	}
	if len(head) == 0 {
		return "", ErrPrefetchUnsupportedMIME
	}
	sniffed := http.DetectContentType(head)
	if idx := strings.Index(sniffed, ";"); idx != -1 {
		sniffed = strings.TrimSpace(sniffed[:idx])
	}
	if _, err := ValidatePrefetchMIME(sniffed); err != nil {
		return "", err
	}
	return sniffed, nil
}

func isLiteralLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := netguard.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// newPrefetchClient builds the prefetch HTTP client: a bounded dialer that
// refuses private/reserved targets after DNS resolution (defeating DNS
// rebinding), and a redirect policy that re-validates every hop.
func newPrefetchClient(allowLoopback bool) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		ControlContext: func(_ context.Context, network, address string, c syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				host = address
			}
			// netguard.ParseIP strips an IPv6 zone. Without it the zoned
			// literal parses to nil, the hook returns nil ("not an address"),
			// and the dial reaches a loopback or metadata target the
			// pre-flight check was supposed to have refused.
			ip := netguard.ParseIP(host)
			if ip == nil {
				return nil
			}
			if ip.IsLoopback() && allowLoopback {
				return nil
			}
			if netguard.IsPrivateOrReserved(ip) {
				return ErrPrefetchSSRFBlocked
			}
			return nil
		},
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			MaxIdleConns:          4,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// via holds the requests already made, so the hop being decided
			// here is len(via)+1. Refusing at len(via) > 3 follows three
			// redirects and stops the fourth, which is what the discovery doc
			// means by a cap of 3.
			if len(via) > 3 {
				return ErrPrefetchSSRFBlocked
			}
			// The loopback exemption has to travel with the redirect. Without
			// it a caller that opted into loopback — a test server, a local
			// image service — could make its first request and then have every
			// redirect refused as an SSRF attempt.
			if allowLoopback && isLiteralLoopbackHost(req.URL.Hostname()) {
				return nil
			}
			if status, err := ValidatePrefetchURL(req.URL.String()); err != nil {
				log.Warn("translator", "image prefetch redirect refused", "status", status, "error", err)
				return err
			}
			if req.URL.Scheme != "https" {
				return ErrPrefetchInvalidScheme
			}
			return nil
		},
	}
}
