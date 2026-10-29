package media

import (
	"bytes"
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// UC web (uncensored.com persona) media provider, ported from upstream
// open-sse/executors/uc-web.js (38a0c449, part of #624): image and video
// generation over the un-metered internal API. The credential is the full
// uncensored.com Cookie header; a short-lived Clerk `__session` JWT is minted
// from the durable `__client` cookie per request, and the account uid comes
// from the minted token's claims.

const (
	ucOrigin       = "https://uncensored.com"
	ucClerkJSVer   = "5.127.1"
	ucPollTimeout  = 300 * time.Second
	ucImageMode    = "dev"
	ucModeration   = "SUPER_LIGHT"
	ucCountry      = "US"
	ucDefaultImage = "seedream-v4.5"
	ucDefaultVideo = "default"
	ucBrowserUA    = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36"
)

// ucImageModel is one image catalog entry; Aspects empty means any ratio,
// Resolutions non-empty pins the `resolution` field.
type ucImageModel struct {
	Name        string
	Upstream    string
	Aspects     []string
	Resolutions []string
}

// ucVideoModel is one video catalog entry; T2V/I2V gate the input image.
type ucVideoModel struct {
	Name        string
	T2V, I2V    bool
	Audio       bool
	Aspects     []string
	Resolutions []string
	Seconds     []int
}

// ucAspectDims maps an aspect ratio to the pixel size /v2/image-gen accepts.
var ucAspectDims = map[string][2]string{
	"1:1":  {"1024", "1024"},
	"16:9": {"1024", "576"},
	"9:16": {"576", "1024"},
	"4:3":  {"1024", "768"},
	"3:4":  {"768", "1024"},
	"3:2":  {"1152", "768"},
	"2:3":  {"768", "1152"},
	"4:5":  {"896", "1120"},
	"5:4":  {"1120", "896"},
	"21:9": {"1344", "576"},
}

// ucImageCatalog mirrors UC_IMAGE_MODELS: ids are the model_version values
// /v2/image-gen accepts.
var ucImageCatalog = map[string]ucImageModel{
	"seedream-v4.5":             {Name: "Seedream v4.5", Aspects: []string{"1:1", "16:9", "9:16", "4:3", "3:4"}},
	"seedream-v5":               {Name: "Seedream v5", Aspects: []string{"1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3"}},
	"seedream-v5-flash":         {Name: "Seedream v5 Flash", Aspects: []string{"1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3", "4:5", "5:4", "21:9"}, Resolutions: []string{"1k", "1.5k", "2k"}},
	"nano-banana-pro-ultra":     {Name: "Nano Banana Pro Ultra", Aspects: []string{"1:1", "3:2", "2:3", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"}, Resolutions: []string{"4k", "8k"}},
	"nano-banana-pro":           {Name: "Nano Banana Pro", Aspects: []string{"1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"}, Resolutions: []string{"1k", "2k", "4k"}},
	"nano-banana":               {Name: "Nano Banana", Aspects: []string{"1:1", "9:16", "16:9", "3:4", "4:3", "3:2", "2:3", "5:4", "4:5", "21:9"}},
	"nano-banana-2":             {Name: "Nano Banana 2", Aspects: []string{"1:1", "3:2", "2:3", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"}, Resolutions: []string{"1k", "2k", "4k"}},
	"gpt-image":                 {Name: "GPT Image 1.5", Aspects: []string{"1:1", "2:3", "3:2"}},
	"gpt-image-2":               {Name: "GPT Image 2", Aspects: []string{"1:1", "3:2", "2:3", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"}},
	"gpt-image-2.5-flare":       {Name: "GPT Image 2.5 Flare", Aspects: []string{"1:1", "3:2", "2:3", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9", "2:1", "1:2", "3:1", "1:3", "9:21"}},
	"gpt-image-2.5-sunburst":    {Name: "GPT Image 2.5 Sunburst", Aspects: []string{"1:1", "3:2", "2:3", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9", "2:1", "1:2", "3:1", "1:3", "9:21"}},
	"wan-2.6-image":             {Name: "Wan 2.6", Upstream: "wan-2.6", Aspects: []string{"1:1", "4:3", "3:4", "3:2", "2:3"}},
	"wan-2.7-text-to-image":     {Name: "Wan 2.7", Aspects: []string{"1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3"}},
	"wan-2.7-text-to-image-pro": {Name: "Wan 2.7 Pro", Aspects: []string{"1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3"}},
	"prefect-pony-xl":           {Name: "Prefect Pony XL", Aspects: []string{"1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3"}},
	"1.0":                       {Name: "Flux.1"},
	"1.1":                       {Name: "Flux.1.1"},
	"2.0":                       {Name: "Flux.2"},
	"flux-2-max":                {Name: "Flux 2 Max"},
	"SDXL":                      {Name: "SDXL uncensored"},
	"realism":                   {Name: "Realism"},
	"realism-2":                 {Name: "Realism 2"},
	"z-image-turbo":             {Name: "Z Image Turbo"},
}

// ucVideoCatalog mirrors UC_VIDEO_MODELS: ids are the model values
// /image_to_video accepts.
var ucVideoCatalog = map[string]ucVideoModel{
	"default":                      {Name: "Standard", T2V: true, I2V: true, Aspects: []string{"auto", "16:9", "9:16", "1:1"}, Resolutions: []string{"480p", "720p"}, Seconds: []int{5, 8}},
	"turbo":                        {Name: "Turbo", T2V: true, I2V: true, Aspects: []string{"auto", "16:9", "9:16", "1:1"}, Resolutions: []string{"480p", "720p"}, Seconds: []int{5, 8}},
	"wan-2.2-uncensored-v2":        {Name: "Wan Ultra Uncensored v2", T2V: true, I2V: true, Aspects: []string{"auto", "16:9", "9:16", "1:1"}, Resolutions: []string{"480p", "720p"}, Seconds: []int{5, 8}},
	"wan-2.2-uncensored":           {Name: "Wan Ultra Uncensored", T2V: true, I2V: true, Aspects: []string{"auto", "16:9", "9:16", "1:1"}, Resolutions: []string{"480p", "720p"}, Seconds: []int{5, 8}},
	"wan-2.6":                      {Name: "Wan v2.6", T2V: true, I2V: true, Aspects: []string{"auto", "16:9", "9:16", "1:1"}, Seconds: []int{5, 8}},
	"wan-2.6-ultra":                {Name: "Wan 2.6 Ultra Uncensored", T2V: true, I2V: true},
	"wan-2.7":                      {Name: "Wan 2.7", T2V: true, I2V: true},
	"wan-2.7-pro":                  {Name: "Wan 2.7 Pro", T2V: true, I2V: true},
	"wan-2.7-uncensored":           {Name: "Wan 2.7 Uncensored", T2V: true, I2V: true},
	"wan-2.7-uncensored-v2":        {Name: "Wan 2.7 Uncensored v2", T2V: true, I2V: true},
	"wan-3.0":                      {Name: "Wan 3.0", T2V: true, I2V: true, Audio: true},
	"wan-3.0-uncensored":           {Name: "Wan 3.0 Uncensored", T2V: true, I2V: true, Audio: true},
	"wan-3.0-uncensored-pro":       {Name: "Wan 3.0 Uncensored Pro", T2V: true, I2V: true, Audio: true},
	"sora-2":                       {Name: "Sora 2", T2V: true, I2V: true, Seconds: []int{4, 8, 12}},
	"sora-2-pro":                   {Name: "Sora 2 Pro", T2V: true, I2V: true, Seconds: []int{4, 8, 12}},
	"grok-imagine-video":           {Name: "Grok Imagine Video", T2V: true, I2V: true},
	"bytedance-seedance-2.0":       {Name: "Seedance 2.0", T2V: true, I2V: true},
	"seedance-2.0-fast":            {Name: "Seedance 2.0 Fast", T2V: true, I2V: true},
	"seedance-2.0-uncensored":      {Name: "Seedance 2.0 Uncensored", T2V: true, I2V: true, Audio: true},
	"seedance-2.0-fast-uncensored": {Name: "Seedance 2.0 Fast Uncensored", T2V: true, I2V: true, Audio: true},
	"seedance-2.0-mini-uncensored": {Name: "Seedance 2.0 Mini Uncensored", T2V: true, I2V: true, Audio: true},
	"seedance-2.0-mini-turbo":      {Name: "Seedance 2.0 Mini Turbo Uncensored", T2V: true, I2V: true, Audio: true},
	"seedance-v1.5-pro-uncensored": {Name: "Seedance v1.5 Pro Uncensored", T2V: true, I2V: true, Audio: true},
	"seedance-2.5-uncensored":      {Name: "Seedance 2.5 Uncensored", T2V: true, I2V: true, Audio: true},
	"seedance-2.5":                 {Name: "Seedance 2.5", T2V: true, I2V: true, Audio: true},
	"vidu-q3-uncensored":           {Name: "Vidu Q3 Uncensored", T2V: true, I2V: true},
	"av-gen-v2":                    {Name: "AV-Gen v2 with audio", T2V: true, I2V: true, Audio: true},
	"cosmos-predict":               {Name: "Cosmos Predict 2.5", T2V: true, I2V: true},
	"minimax-h3":                   {Name: "MiniMax H3", T2V: true, I2V: true},
	"happyhorse-1.0":               {Name: "Happy Horse 1.0", T2V: true, I2V: true},
	"wan/v2.2-a14b":                {Name: "Wan v2.2-a14b", T2V: true, I2V: true},
}

// ucMediaError carries the HTTP status the caller must answer with.
type ucMediaError struct {
	status  int
	message string
}

func (e *ucMediaError) Error() string { return e.message }

func ucFail(status int, format string, args ...any) *ucMediaError {
	return &ucMediaError{status: status, message: fmt.Sprintf(format, args...)}
}

// ucCookieJar is a parsed Cookie header.
type ucCookieJar map[string]string

func parseUcCookieJar(header string) ucCookieJar {
	jar := ucCookieJar{}
	for _, part := range strings.Split(header, ";") {
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			continue
		}
		name := strings.TrimSpace(part[:eq])
		if name != "" {
			jar[name] = strings.TrimSpace(part[eq+1:])
		}
	}
	return jar
}

func (j ucCookieJar) header() string {
	parts := make([]string, 0, len(j))
	for name, value := range j {
		parts = append(parts, name+"="+value)
	}
	return strings.Join(parts, "; ")
}

// ucDecodeJWT reads the payload claims without verifying the signature; the
// token was minted by Clerk this same request (or is the durable client JWT).
func ucDecodeJWT(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some issuers pad; tolerate the standard alphabet too.
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}

// The persona endpoints are fixed HTTPS origins in production; they are vars
// so the test suite can aim them at fakes.
var (
	ucHTTPClient   = &http.Client{Timeout: 60 * time.Second}
	ucClerkFAPI    = "https://clerk.uncensored.com"
	ucImageURL     = "https://internal.chatuncensored.ai/v2/image-gen"
	ucVideoURL     = "https://internal.chatuncensored.ai/image_to_video"
	ucSignedURL    = "https://internal-6.pubyar.com/generate-signed-url"
	ucPollInterval = 5 * time.Second
)

// ucSession is a minted Clerk session.
type ucSession struct {
	jwt           string
	uid           string
	rotatedClient string
}

// ucMintSession mints the 60s Clerk __session JWT from the durable __client
// cookie. Newer Clerk clients ship a __client JWT without a sid claim, so the
// session id falls back to GET /v1/client's session list; Clerk rotates
// __client on every call, and the rotated value is returned so the caller can
// persist it back onto the connection.
func ucMintSession(ctx context.Context, cookieHeader string) (*ucSession, error) {
	jar := parseUcCookieJar(cookieHeader)
	client, hasClient := jar["__client"]
	if !hasClient || client == "" {
		return nil, ucFail(401, "UC credential missing the __client cookie — paste the full uncensored.com Cookie header from a logged-in session")
	}
	clerkHeaders := func() map[string]string {
		return map[string]string{
			"Cookie":  jar.header(),
			"Origin":  ucOrigin,
			"Referer": ucOrigin + "/",
			"Accept":  "application/json",
		}
	}

	sid := ""
	rotated := ""
	if claims := ucDecodeJWT(client); claims != nil {
		if s, _ := claims["sid"].(string); s != "" {
			sid = s
		}
	}
	if sid == "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			ucClerkFAPI+"/v1/client?_clerk_js_version="+ucClerkJSVer, nil)
		if err == nil {
			for k, v := range clerkHeaders() {
				req.Header.Set(k, v)
			}
			if resp, err := ucHTTPClient.Do(req); err == nil {
				rotated = ucReadSetCookie(resp, "__client")
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				_ = resp.Body.Close()
				var payload struct {
					Response struct {
						Sessions []struct {
							ID     string `json:"id"`
							Status string `json:"status"`
						} `json:"sessions"`
					} `json:"response"`
					Client struct {
						Sessions []struct {
							ID     string `json:"id"`
							Status string `json:"status"`
						} `json:"sessions"`
					} `json:"client"`
					Sessions []struct {
						ID     string `json:"id"`
						Status string `json:"status"`
					} `json:"sessions"`
				}
				if json.Unmarshal(body, &payload) == nil {
					sessions := payload.Response.Sessions
					if len(sessions) == 0 {
						sessions = payload.Client.Sessions
					}
					if len(sessions) == 0 {
						sessions = payload.Sessions
					}
					for _, s := range sessions {
						if s.Status == "active" || sid == "" {
							if s.Status == "active" {
								sid = s.ID
							} else if sid == "" {
								sid = s.ID
							}
						}
					}
				}
			}
		}
	}
	if sid == "" {
		return nil, ucFail(401, "UC Clerk session not found — the __client cookie has no active session. Log in again and re-paste the Cookie header")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		ucClerkFAPI+"/v1/client/sessions/"+sid+"/tokens?_clerk_js_version="+ucClerkJSVer,
		strings.NewReader(""))
	if err != nil {
		return nil, ucFail(502, "UC Clerk token mint failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ucOrigin)
	req.Header.Set("Referer", ucOrigin+"/")
	req.Header.Set("Cookie", jar.header())
	resp, err := ucHTTPClient.Do(req)
	if err != nil {
		return nil, ucFail(502, "UC Clerk token mint failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return nil, ucFail(resp.StatusCode, "UC Clerk token mint failed (HTTP %d) %s", resp.StatusCode, detail)
	}
	if rotatedNow := ucReadSetCookie(resp, "__client"); rotatedNow != "" {
		rotated = rotatedNow
	}
	var body struct {
		JWT string `json:"jwt"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if json.Unmarshal(raw, &body) != nil || body.JWT == "" {
		return nil, ucFail(502, "UC Clerk token mint returned no jwt")
	}
	uid := ""
	if claims := ucDecodeJWT(body.JWT); claims != nil {
		uid, _ = claims["uid"].(string)
	}
	return &ucSession{jwt: body.JWT, uid: uid, rotatedClient: rotated}, nil
}

// ucReadSetCookie reads one Set-Cookie value off a response.
func ucReadSetCookie(resp *http.Response, name string) string {
	for _, cookie := range resp.Cookies() {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

// ucWithRotatedClient swaps the __client value inside a stored Cookie header;
// "" means no rotation happened.
func ucWithRotatedClient(cookieHeader, rotated string) string {
	if rotated == "" {
		return ""
	}
	jar := parseUcCookieJar(cookieHeader)
	if jar["__client"] == rotated {
		return ""
	}
	jar["__client"] = rotated
	return jar.header()
}

// ucAspectFor resolves the size parameter to an aspect ratio and pixel size,
// honouring the model's allowed ratios (empty = any). A WxH size picks the
// closest allowed ratio; anything unrecognised falls back to the first
// allowed ratio (or 1:1).
func ucAspectFor(size string, allowed []string) map[string]any {
	allowedSet := map[string]bool{}
	for _, a := range allowed {
		allowedSet[a] = true
	}
	isAllowed := func(aspect string) bool {
		if len(allowedSet) == 0 {
			return true
		}
		return allowedSet[aspect]
	}
	pick := func(aspect string) map[string]any {
		dims, ok := ucAspectDims[aspect]
		if !ok || !isAllowed(aspect) {
			return nil
		}
		return map[string]any{
			"aspect_ratio": aspect,
			"imageWidth":   dims[0],
			"imageHeight":  dims[1],
		}
	}
	raw := strings.TrimSpace(size)
	if direct := pick(raw); direct != nil {
		return direct
	}
	if w, h, ok := strings.Cut(raw, "x"); ok {
		width, errW := strconv.ParseFloat(w, 64)
		height, errH := strconv.ParseFloat(h, 64)
		if errW == nil && errH == nil && width > 0 && height > 0 {
			best, bestDelta := "", 1e9
			for aspect, dims := range ucAspectDims {
				if !isAllowed(aspect) {
					continue
				}
				aw, _ := strconv.ParseFloat(dims[0], 64)
				ah, _ := strconv.ParseFloat(dims[1], 64)
				delta := aw/ah - width/height
				if delta < 0 {
					delta = -delta
				}
				if delta < bestDelta {
					bestDelta, best = delta, aspect
				}
			}
			if best != "" {
				return pick(best)
			}
		}
	}
	fallback := "1:1"
	if len(allowed) > 0 {
		fallback = allowed[0]
	}
	if result := pick(fallback); result != nil {
		return result
	}
	return pick("1:1")
}

// ucPollResult HEAD-polls the pre-determined result URL until it serves 200.
// A 4xx other than 429 is logged and polling continues — the artifact can
// appear after a transient probe failure.
func ucPollResult(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(ucPollInterval):
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Referer", ucOrigin+"/")
		resp, err := ucHTTPClient.Do(req)
		if err != nil {
			continue // transient network — keep polling
		}
		status := resp.StatusCode
		_ = resp.Body.Close()
		if status == http.StatusOK {
			return nil
		}
		if status >= 400 && status < 500 && status != http.StatusTooManyRequests {
			log.Warn("media", "uc-web result poll got HTTP status", "status", status)
		}
	}
	return errors.New("UC result did not become available in time")
}

// ucDownload fetches the finished artifact as base64.
func ucDownload(ctx context.Context, url string) (b64, mime string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Referer", ucOrigin+"/")
	resp, err := ucHTTPClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", "", ucFail(502, "UC result download failed (HTTP %d)", resp.StatusCode)
	}
	buf, err := io.ReadAll(io.LimitReader(resp.Body, 100<<20))
	if err != nil {
		return "", "", err
	}
	mime = strings.Split(resp.Header.Get("Content-Type"), ";")[0]
	if mime == "" {
		mime = "application/octet-stream"
	}
	return base64.StdEncoding.EncodeToString(buf), mime, nil
}

var ucDataURLRE = regexp.MustCompile(`^data:[^;]*;base64,(.*)$`)

// ucResolveImageBytes decodes an input image reference (data URL, http(s) URL,
// or bare base64).
func ucResolveImageBytes(ctx context.Context, ref string) ([]byte, error) {
	if m := ucDataURLRE.FindStringSubmatch(ref); m != nil {
		return base64.StdEncoding.DecodeString(m[1])
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "image/*")
		req.Header.Set("User-Agent", ucBrowserUA)
		resp, err := ucHTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, ucFail(502, "input image download failed (HTTP %d)", resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	}
	return base64.StdEncoding.DecodeString(ref)
}

// handleUcWebImage serves /v1/images/generations for uc-web.
func (h *MediaHandler) handleUcWebImage(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	var req struct {
		Prompt     string `json:"prompt"`
		Model      string `json:"model"`
		Size       string `json:"size"`
		Resolution string `json:"resolution"`
	}
	_ = json.Unmarshal(body, &req)
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return fmt.Errorf("missing required field: prompt")
	}
	model := modelInfo.Model
	if model == "" {
		model = req.Model
		model = strings.TrimPrefix(model, "uc-web/")
		model = strings.TrimPrefix(model, "uc/")
	}
	if model == "" || model == "uc-web" || model == "uc" {
		model = ucDefaultImage
	}
	spec, known := ucImageCatalog[model]
	if !known {
		return ucFail(400, "Unknown UC image model %q — see the provider's model list", model)
	}

	b64, mime, err := h.withUcSession(r.Context(), func(session *ucSession) (string, string, error) {
		headers := map[string]string{
			"Authorization": "Bearer " + session.jwt,
			"Origin":        ucOrigin,
			"Referer":       ucOrigin + "/",
			"Content-Type":  "application/json",
		}
		payload := map[string]any{
			"prompt":         prompt,
			"mode":           ucImageMode,
			"model_version":  spec.upstream(model),
			"m_n_user":       true,
			"moderationMode": ucModeration,
			"country":        ucCountry,
			"vdiscount":      false,
		}
		for k, v := range ucAspectFor(req.Size, spec.Aspects) {
			payload[k] = v
		}
		if len(spec.Resolutions) > 0 {
			want := strings.ToLower(req.Resolution)
			resolution := spec.Resolutions[0]
			for _, candidate := range spec.Resolutions {
				if candidate == want {
					resolution = want
					break
				}
			}
			payload["resolution"] = resolution
		}
		resultURL, timeout, err := ucPostJSON(r.Context(), ucImageURL, headers, payload)
		if err != nil {
			return "", "", err
		}
		if err := ucPollResult(r.Context(), resultURL, timeout); err != nil {
			return "", "", ucFail(504, "%s", err.Error())
		}
		return ucDownload(r.Context(), resultURL)
	})
	if err != nil {
		return err
	}
	return writeUcMediaJSON(w, b64, mime)
}

// handleUcWebVideo serves /v1/videos/generations for uc-web. Text-to-video
// posts a null media blob; image-to-video uploads the input image through the
// signed-URL endpoint first.
func (h *MediaHandler) handleUcWebVideo(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	var req struct {
		Prompt     string   `json:"prompt"`
		Model      string   `json:"model"`
		Size       string   `json:"size"`
		Seconds    any      `json:"seconds"`
		Resolution string   `json:"resolution"`
		Image      string   `json:"image"`
		Images     []string `json:"images"`
	}
	_ = json.Unmarshal(body, &req)
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return fmt.Errorf("missing required field: prompt")
	}
	model := modelInfo.Model
	if model == "" {
		model = req.Model
		model = strings.TrimPrefix(model, "uc-web/")
		model = strings.TrimPrefix(model, "uc/")
	}
	if model == "" {
		model = ucDefaultVideo
	}
	spec, known := ucVideoCatalog[model]
	if !known {
		return ucFail(400, "Unknown UC video model %q — see the provider's model list", model)
	}
	inputImage := req.Image
	if inputImage == "" && len(req.Images) > 0 {
		inputImage = req.Images[0]
	}
	if inputImage != "" && !spec.I2V {
		return ucFail(400, "UC video model %q does not accept an input image", model)
	}
	if inputImage == "" && !spec.T2V {
		return ucFail(400, "UC video model %q requires an input image", model)
	}
	seconds := 5
	if s, err := toPositiveInt(req.Seconds); err == nil {
		seconds = s
	} else if len(spec.Seconds) > 0 {
		seconds = spec.Seconds[0]
	}

	b64, mime, err := h.withUcSession(r.Context(), func(session *ucSession) (string, string, error) {
		headers := map[string]string{
			"Authorization": "Bearer " + session.jwt,
			"Origin":        ucOrigin,
			"Referer":       ucOrigin + "/",
			"Content-Type":  "application/json",
		}
		videoResolution := "480p"
		if len(spec.Resolutions) > 0 {
			videoResolution = spec.Resolutions[0]
			for _, candidate := range spec.Resolutions {
				if candidate == req.Resolution {
					videoResolution = req.Resolution
					break
				}
			}
		}
		payload := map[string]any{
			"prompt":                  prompt,
			"media_blob_name":         nil,
			"num_frames":              seconds*16 + 1,
			"frames_per_second":       16,
			"num_inference_steps":     30,
			"guide_scale":             5,
			"shift":                   5,
			"aspect_ratio":            ucVideoAspect(spec),
			"pro_mode":                model == "wan-3.0-uncensored-pro",
			"turbo":                   model == "turbo",
			"resolution":              videoResolution,
			"sora_resolution":         videoResolution,
			"end_frame_blob_name":     nil,
			"model":                   model,
			"seconds":                 seconds,
			"video_to_video_duration": seconds,
			"vdiscount":               false,
		}

		if inputImage != "" {
			if session.uid == "" {
				return "", "", ucFail(401, "Could not derive the account uid — re-paste the full Cookie header")
			}
			imageBytes, err := ucResolveImageBytes(r.Context(), inputImage)
			if err != nil {
				return "", "", err
			}
			var signed struct {
				SignedURL string `json:"signed_url"`
				BlobName  string `json:"blob_name"`
			}
			if err := ucPostJSONInto(r.Context(), ucSignedURL, headers, map[string]any{
				"content_type":    "image/png",
				"user_identifier": session.uid,
			}, &signed); err != nil {
				return "", "", err
			}
			if signed.SignedURL == "" || signed.BlobName == "" {
				return "", "", ucFail(502, "UC signed-url response missing signed_url or blob_name")
			}
			putReq, err := http.NewRequestWithContext(r.Context(), http.MethodPut, signed.SignedURL, bytes.NewReader(imageBytes))
			if err != nil {
				return "", "", err
			}
			putReq.Header.Set("Content-Type", "image/png")
			putResp, err := ucHTTPClient.Do(putReq)
			if err != nil {
				return "", "", ucFail(502, "UC input-image upload failed: %v", err)
			}
			_ = putResp.Body.Close()
			if putResp.StatusCode != http.StatusOK {
				return "", "", ucFail(putResp.StatusCode, "UC input-image upload failed (HTTP %d)", putResp.StatusCode)
			}
			payload["media_blob_name"] = signed.BlobName
		}

		resultURL, timeout, err := ucPostJSON(r.Context(), ucVideoURL, headers, payload)
		if err != nil {
			return "", "", err
		}
		if err := ucPollResult(r.Context(), resultURL, timeout); err != nil {
			return "", "", ucFail(504, "%s", err.Error())
		}
		return ucDownload(r.Context(), resultURL)
	})
	if err != nil {
		return err
	}
	return writeUcMediaJSON(w, b64, mime)
}

// withUcSession mints the Clerk session over a uc-web connection, persists a
// rotated __client cookie, and runs fn with the minted JWT.
func (h *MediaHandler) withUcSession(ctx context.Context, fn func(*ucSession) (string, string, error)) (string, string, error) {
	conn, connData, err := h.ChatH.GetBestConnectionWithContext(ctx, "uc-web", "", nil, "")
	if err != nil || connData == nil {
		return "", "", fmt.Errorf("no active connection for uc-web: %w", err)
	}
	cookieHeader := chat.ExtractAPIKey(connData)
	session, err := ucMintSession(ctx, cookieHeader)
	if err != nil {
		return "", "", err
	}
	if next := ucWithRotatedClient(cookieHeader, session.rotatedClient); next != "" && conn != nil && h.Repo != nil {
		if data := updateUcCookieHeader(conn.Data, next); data != "" {
			_ = h.Repo.UpdateConnectionData(conn.ID, data)
		}
	}
	if h.Repo != nil && conn != nil {
		_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
	}
	return fn(session)
}

// updateUcCookieHeader swaps the apiKey inside a connection's stored data
// JSON; "" means nothing changed or the row could not be rewritten.
func updateUcCookieHeader(connData, nextCookie string) string {
	if connData == "" {
		return ""
	}
	var m map[string]any
	if json.Unmarshal([]byte(connData), &m) != nil || m == nil {
		return ""
	}
	m["apiKey"] = nextCookie
	out, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(out)
}

// ucPostJSON posts a JSON body and reads the response's result URL plus its
// optional timeout_seconds.
func ucPostJSON(ctx context.Context, url string, headers map[string]string, payload map[string]any) (string, time.Duration, error) {
	var out struct {
		URL            string  `json:"url"`
		TimeoutSeconds float64 `json:"timeout_seconds"`
	}
	if err := ucPostJSONInto(ctx, url, headers, payload, &out); err != nil {
		return "", 0, err
	}
	if out.URL == "" {
		return "", 0, ucFail(502, "UC response carried no result url")
	}
	timeout := ucPollTimeout
	if out.TimeoutSeconds > 0 {
		timeout = time.Duration(out.TimeoutSeconds * float64(time.Second))
	}
	return out.URL, timeout, nil
}

func ucPostJSONInto(ctx context.Context, url string, headers map[string]string, payload map[string]any, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, fmt.Sprint(v))
	}
	resp, err := ucHTTPClient.Do(req)
	if err != nil {
		return ucFail(502, "UC request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return ucFail(resp.StatusCode, "UC request failed (HTTP %d) %s", resp.StatusCode, detail)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return ucFail(502, "UC response was not valid JSON")
	}
	return nil
}

// ucVideoAspect picks the video aspect for the model's allowed list.
func ucVideoAspect(spec ucVideoModel) string {
	for _, aspect := range spec.Aspects {
		if aspect == "auto" {
			return "auto"
		}
	}
	if len(spec.Aspects) > 0 {
		return spec.Aspects[0]
	}
	return "9:16"
}

// writeUcMediaJSON answers in the OpenAI media shape with one b64 artifact.
func writeUcMediaJSON(w http.ResponseWriter, b64, mime string) error {
	entry := map[string]any{"b64_json": b64}
	if mime != "" {
		entry["mime_type"] = mime
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	out, err := json.Marshal(map[string]any{
		"created": time.Now().Unix(),
		"data":    []any{entry},
	})
	if err != nil {
		return err
	}
	_, _ = w.Write(out)
	return nil
}

func toPositiveInt(v any) (int, error) {
	switch n := v.(type) {
	case float64:
		if n > 0 {
			return int(n), nil
		}
	case int:
		if n > 0 {
			return n, nil
		}
	case string:
		if parsed, err := strconv.Atoi(n); err == nil && parsed > 0 {
			return parsed, nil
		}
	}
	return 0, errors.New("not a positive number")
}

// upstream resolves the model_version: the explicit override or the catalog
// id the caller looked up.
func (m ucImageModel) upstream(id string) string {
	if m.Upstream != "" {
		return m.Upstream
	}
	return id
}

// writeUcMediaError answers with the typed status a ucMediaError carries, or
// 502 for any other failure shape.
func writeUcMediaError(w http.ResponseWriter, err error) {
	var ucErr *ucMediaError
	if errors.As(err, &ucErr) {
		handlerutil.WriteJSONError(w, ucErr.status, ucErr.message)
		return
	}
	handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
}

// ValidateUcWebCookie probes a pasted Cookie header by minting a Clerk
// session from it: the same first step every media request takes, without
// spending a generation.
func ValidateUcWebCookie(ctx context.Context, apiKey string) (valid bool, message string) {
	if _, err := ucMintSession(ctx, apiKey); err != nil {
		return false, err.Error()
	}
	return true, ""
}
