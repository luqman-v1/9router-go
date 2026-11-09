package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	json "encoding/json/v2"

	fhttp "github.com/bogdanfinn/fhttp"

	"9router/proxy/internal/log"

	"9router/proxy/internal/providers"
)

var (
	// The bootstrap page carries its session tokens in an inline
	// `WIZ_global_data` object. They are read out of the page rather than
	// requested, which is the whole reason there is no separate auth call.
	geminiWebBlRe   = regexp.MustCompile(`"cfb2h":"([^"]+)"`)
	geminiWebFSidRe = regexp.MustCompile(`"FdrFJe":"([^"]+)"`)
	// geminiWebAtRe is the anti-CSRF token the site posts as the `at` form
	// field. A signed-in page carries it; an anonymous one does not, which is
	// why it is read opportunistically rather than required — see
	// geminiWebTokens for what its absence means.
	geminiWebAtRe = regexp.MustCompile(`"SNlM0e":"([^"]+)"`)
	// geminiWebPushIDRe is the feed id the page carries. It ties an uploaded
	// image to the session that uploaded it, so it is read alongside the other
	// page tokens.
	geminiWebPushIDRe = regexp.MustCompile(`"(?:qKIAYe|q2k)":"(feeds/[a-z0-9]+)"`)
)

// Gemini Web answers an anonymous visitor: `gemini.google.com/app` issues the
// session tokens to a plain HTTP client, and StreamGenerate serves a turn with
// no credential at all. That was verified live before this was written, and it
// is why this executor needs no browser, no TLS impersonation and no cookie
// pasting — the three things the bridge this was modelled on requires.
//
// A pasted cookie header is still honoured when one is configured, because a
// signed-in session gets the paid models while an anonymous one is served
// Flash-Lite only. The credential is therefore optional rather than required,
// and its absence costs model choice, not reachability.
const (
	// geminiWebFirstByteTimeout bounds the wait for the first stream bytes.
	geminiWebFirstByteTimeout = 60 * time.Second
	// geminiWebIdleTimeout bounds the gap between frames once a turn is under way.
	geminiWebIdleTimeout = 90 * time.Second
	// geminiWebPageTimeout bounds the bootstrap page fetch.
	geminiWebPageTimeout = 30 * time.Second
	// geminiWebRPCTimeout bounds a discovery RPC. Discovery is on the request
	// path only when the model list is unknown, so it cannot be unbounded.
	geminiWebRPCTimeout = 30 * time.Second
)

// geminiWebEndpoints is where one connection's turn is addressed.
//
// The origin is taken from the connection's configured base URL when it names
// one, so a test (and a user behind a mirror) can point the turn somewhere other
// than the public site. Every derived URL comes from it: a request that left for
// a configured host while the headers still named the public one would be both
// incoherent and detectable.
type geminiWebEndpoints struct {
	origin string
	app    string
	batch  string
	stream string
	// upload is the content-push service an image is uploaded to. It is a
	// different host from the site — the site's own composer uploads
	// cross-site to it — so it is not derived from the origin.
	upload string
	// uploadFallback is the resumable service the primary upload falls back
	// to. It is a different host too, for the same reason.
	uploadFallback string
}

// geminiWebUploadEndpointOverride points the content-push service somewhere
// else.
//
// It exists so a test can serve the upload from a second server, which is the
// only way to prove that the session's cookies travel to a host other than
// the site's — a jar is per-host, and a test whose upload goes to the same
// host would pass whether the copy happened or not.
var geminiWebUploadEndpointOverride atomic.Pointer[string]

// geminiWebEndpointsFor resolves the endpoints for one connection.
func geminiWebEndpointsFor(cfg *providers.ProviderConfig) geminiWebEndpoints {
	origin := geminiWebOrigin
	if cfg != nil && strings.TrimSpace(cfg.BaseURL) != "" {
		if parsed := geminiWebOriginOf(cfg.BaseURL); parsed != "" {
			origin = parsed
		}
	}
	upload := geminiWebUploadEndpoint
	fallback := geminiWebUploadFallback
	if origin != geminiWebOrigin {
		// A mirror (or a test) is one server, so the upload goes there too:
		// the public content-push host is reachable from a public origin and
		// from nowhere else. The fallback follows, so a mirror's image bytes
		// are never sent to a host the operator did not name.
		upload = origin + geminiWebUploadPath
		fallback = origin + geminiWebUploadFallbackPath
	}
	if override := geminiWebUploadEndpointOverride.Load(); override != nil {
		upload = *override
	}
	return geminiWebEndpoints{
		origin:         origin,
		app:            origin + "/app",
		batch:          origin + geminiWebBatchPath,
		stream:         origin + geminiWebStreamPath,
		upload:         upload,
		uploadFallback: fallback,
	}
}

// geminiWebOriginOf reduces a configured base URL to its scheme and host.
func geminiWebOriginOf(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

// referer is the page an XHR is issued from. The API calls are made by
// /app's own JavaScript, so that is the page they name.
func (e geminiWebEndpoints) referer() string { return e.app }

// geminiWebRequestID is the process-wide _reqid counter. The site only requires
// that it increase, and a shared counter keeps concurrent turns distinct.
var (
	geminiWebReqID atomic.Int64

	geminiWebSessionMu sync.Mutex
	geminiWebSessions  = map[string]*geminiWebSession{}
)

func init() {
	geminiWebReqID.Store(time.Now().UnixNano() % 900000)
}

// geminiWebSession is the live state one credential's turns share: the cookie
// jar the site sets, the per-page tokens every call echoes back, and the client
// that dials it.
type geminiWebSession struct {
	endpoints geminiWebEndpoints
	// stealth is the TLS/HTTP2 stack the request is dialled with. Go's net/http
	// cannot express Chrome's header order, so this provider does not use it —
	// see gemini_web_stealth.go for why that matters and what it costs.
	stealth *geminiWebStealthClient
	cookie  string
	mu      sync.RWMutex
	page    geminiWebTokens
	fetched time.Time
	// warnedNoAt keeps the "cookie produced no SNlM0e" warning to once per
	// session, since every turn would otherwise repeat it.
	warnedNoAt bool
}

// geminiWebSessionFor returns the session a turn with this credential uses.
//
// Sessions are keyed by credential so one connection's cookies can never be
// presented for another's turn. The anonymous credential is its own key, which
// is also what makes an unconfigured connection work.
func geminiWebSessionFor(req *Request) *geminiWebSession {
	credential := geminiWebCookieHeader(req)
	endpoints := geminiWebEndpointsFor(req.Config)
	key := credential + "\x00" + endpoints.origin
	geminiWebSessionMu.Lock()
	defer geminiWebSessionMu.Unlock()
	if session, ok := geminiWebSessions[key]; ok {
		return session
	}
	stealth, err := newGeminiWebStealthClient(req.Client, endpoints.origin, credential)
	if err != nil {
		// The client is built per session and can only fail on a bad option, so a
		// failure here is a programming error rather than an upstream problem.
		// Falling back to no client at all would turn it into a nil dereference on
		// the next call, which is worse than reporting it now.
		log.Error("gemini-web", "could not build TLS client", "error", err)
	}
	session := &geminiWebSession{endpoints: endpoints, stealth: stealth, cookie: credential}
	geminiWebSessions[key] = session
	return session
}

// geminiWebEvictSession drops the session a credential owns.
//
// A session is keyed by its credential, and a renewed credential is a
// different one, so every renewal would otherwise leave the previous
// session behind. Without this the map grows by one entry per renewal, and
// a rotation every nine minutes is 160 entries a day for one account.
func geminiWebEvictSession(credential string) {
	if credential == "" {
		return
	}
	geminiWebSessionMu.Lock()
	defer geminiWebSessionMu.Unlock()
	for key, session := range geminiWebSessions {
		if !strings.HasPrefix(key, credential+"\x00") {
			continue
		}
		if session != nil && session.stealth != nil {
			session.stealth.closeIdleConnections()
		}
		delete(geminiWebSessions, key)
	}
}

// geminiWebTokens are the per-page tokens every later call echoes back.
type geminiWebTokens struct {
	// bl and fSid are required: without them a call cannot be formed at all.
	bl   string
	fSid string
	// at is the anti-CSRF token posted as the `at` form field. The site issues
	// it to a signed-in page and omits it for an anonymous visitor, so it is
	// optional — but when a cookie is configured and this is still empty the
	// cookie bought nothing, which is worth reporting rather than silently
	// sending anonymous-shaped requests.
	at string
	// pushID is the feed the page belongs to. It travels with an image
	// upload, and a page that carries none falls back to the site's own
	// default — see geminiWebUploadDefaultPush.
	pushID string
}

// tokens returns the cached page tokens, fetching them when they are absent or
// old enough that the site may have rotated them.
func (s *geminiWebSession) tokens(ctx context.Context) (geminiWebTokens, error) {
	s.mu.RLock()
	page, fetched := s.page, s.fetched
	s.mu.RUnlock()
	if page.bl != "" && page.fSid != "" && time.Since(fetched) < 20*time.Minute {
		return page, nil
	}
	return s.refreshTokens(ctx)
}

// refreshTokens re-reads the page that carries the session tokens.
func (s *geminiWebSession) refreshTokens(ctx context.Context) (geminiWebTokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	requestCtx, cancel := context.WithTimeout(ctx, geminiWebPageTimeout)
	defer cancel()
	request, err := fhttp.NewRequestWithContext(requestCtx, http.MethodGet, s.endpoints.app, nil)
	if err != nil {
		return geminiWebTokens{}, fmt.Errorf("gemini-web: build bootstrap request: %w", err)
	}
	geminiWebSetOrdered(request.Header, geminiWebNavigationOrder,
		geminiWebNavigationValues(s.endpoints.origin, s.cookie))
	response, err := s.stealth.do(request)
	if err != nil {
		return geminiWebTokens{}, fmt.Errorf("gemini-web: fetch %s: %w", s.endpoints.app, err)
	}
	defer response.Body.Close()
	// The page is ~800 KB of inline script; the tokens are read out of it rather
	// than the whole body being buffered.
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return geminiWebTokens{}, fmt.Errorf("gemini-web: read %s: %w", s.endpoints.app, err)
	}
	html := string(body)
	bl := geminiWebBlRe.FindStringSubmatch(html)
	fSid := geminiWebFSidRe.FindStringSubmatch(html)
	if len(bl) < 2 || len(fSid) < 2 || bl[1] == "" || fSid[1] == "" {
		return geminiWebTokens{}, fmt.Errorf("gemini-web: %s did not carry session tokens (HTTP %d)", s.endpoints.app, response.StatusCode)
	}
	tokens := geminiWebTokens{bl: bl[1], fSid: fSid[1]}
	if at := geminiWebAtRe.FindStringSubmatch(html); len(at) >= 2 {
		tokens.at = at[1]
	}
	if pushID := geminiWebPushIDRe.FindStringSubmatch(html); len(pushID) >= 2 {
		tokens.pushID = pushID[1]
	}
	s.page, s.fetched = tokens, time.Now()
	return tokens, nil
}

// invalidateTokens drops the cached page tokens so the next call re-reads them.
func (s *geminiWebSession) invalidateTokens() {
	s.mu.Lock()
	s.fetched = time.Time{}
	s.mu.Unlock()
}

// do issues one request against the site, applying the headers its client sends
// in the order Chrome sends them.
func (s *geminiWebSession) do(ctx context.Context, method, target string, body io.Reader, contentType string, extra map[string]string) (*fhttp.Response, error) {
	request, err := fhttp.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, fmt.Errorf("gemini-web: build request: %w", err)
	}
	// The content-push service is a different host from the site, and a jar is
	// per-host, so the session's cookies are not there to be sent. The site's
	// own composer carries them across, and an upload that arrives without
	// them belongs to no session — which a signed-in account only discovers
	// when the model cannot resolve the attachment.
	s.shareCredentialTo(target)
	values := geminiWebCORSValues(s.endpoints.origin, s.endpoints.referer())
	// The per-call headers are merged into the shared set, so a call that names a
	// header the base set also names takes its own value. Everything supplied is
	// then written in the one known order, with unknown names appended after it.
	for key, value := range extra {
		values[key] = value
	}
	if contentType != "" {
		values["content-type"] = contentType
	}
	// The cookie is not written here: the jar carries it, including any value the
	// site has rotated since the credential was configured. Writing it as well
	// would send the name twice — see geminiWebStealthClient.do.
	geminiWebSetOrdered(request.Header, geminiWebCORSOrder, values)
	request.Header[fhttp.PHeaderOrderKey] = geminiWebPseudoHeaderOrder
	return s.stealth.do(request)
}

// shareCredentialTo copies the session's cookies to the host a request is
// addressed to.
//
// The jar is per-host, so the site's own cookies are not on any other host's
// request. A jar is also the only place a rotated cookie's current value
// lives, so the copy is read from there rather than from the configured
// credential.
func (s *geminiWebSession) shareCredentialTo(target string) {
	if s.stealth == nil {
		return
	}
	s.stealth.shareCredentialTo(target)
}

// geminiWebFormBody renders the urlencoded form both RPCs post, carrying the
// `at` token when the session has one.
//
// `at` is the anti-CSRF value the page's own JavaScript posts alongside `f.req`.
// The site issues it to a signed-in page and omits it for an anonymous visitor,
// and both cases work — but a signed-in page's request without it is not the
// request that page makes, so a session that has one sends it.
func geminiWebFormBody(at, freq string) string {
	body := url.Values{}
	if at != "" {
		body.Set("at", at)
	}
	body.Set("f.req", freq)
	return body.Encode()
}

// streamTurn runs one turn and hands every parsed line to onLine, in order.
func (s *geminiWebSession) streamTurn(
	ctx context.Context,
	prompt string,
	thinkingLevel int,
	model *geminiWebModel,
	attachments []geminiWebAttachment,
	onLine func(*geminiWebLine) error,
) error {
	tokens, err := s.tokens(ctx)
	if err != nil {
		return err
	}
	// A configured cookie that produced no `at` is a cookie the site did not
	// accept: the page it served was the visitor's, so the request would go out
	// anonymous and the model the user asked for would be refused as unserved.
	// Reported here rather than left to surface as a confusing model refusal.
	if tokens.at == "" && s.cookie != "" && !s.warnedNoAt {
		s.warnedNoAt = true
		log.Warn("gemini-web", "the configured cookie produced no SNlM0e token; the site served an anonymous page",
			"hint", "the cookie is likely stale or missing __Secure-1PSID")
	}
	modelNumber := 1
	if model != nil {
		modelNumber = model.Number
	}
	inner := geminiWebBuildInner(prompt, thinkingLevel, modelNumber, attachments)
	uuid := geminiWebRequestUUID()
	inner[geminiWebSlotRequestUUID] = uuid
	innerJSON, err := json.Marshal(inner)
	if err != nil {
		return fmt.Errorf("gemini-web: encode turn: %w", err)
	}
	form, err := json.Marshal([]any{nil, string(innerJSON)})
	if err != nil {
		return fmt.Errorf("gemini-web: encode turn envelope: %w", err)
	}

	query := url.Values{}
	query.Set("hl", "en")
	query.Set("_reqid", geminiWebRequestID())
	query.Set("rt", "c")
	query.Set("bl", tokens.bl)
	query.Set("f.sid", tokens.fSid)

	headers := map[string]string{
		"x-goog-ext-525005358-jspb": fmt.Sprintf("[%q,1]", uuid),
		"x-goog-ext-73010989-jspb":  "[0]",
		"x-goog-ext-73010990-jspb":  "[0,0,0]",
	}
	if model != nil {
		if header := geminiWebBuildModelHeader(*model, thinkingLevel, tokens.fSid); header != "" {
			headers["x-goog-ext-525001261-jspb"] = header
		}
	}

	response, err := s.do(ctx, http.MethodPost,
		s.endpoints.stream+"?"+query.Encode(),
		strings.NewReader(geminiWebFormBody(tokens.at, string(form))),
		"application/x-www-form-urlencoded;charset=utf-8", headers)
	if err != nil {
		return fmt.Errorf("gemini-web: stream turn: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return &geminiWebUpstreamStatus{Status: response.StatusCode, Operation: "StreamGenerate"}
	}

	// Lines are read as they arrive: the whole point of the site's token
	// streaming is that each frame is a complete answer-so-far, so buffering the
	// response would throw away the latency it exists to provide.
	reader := newGeminiWebLineReader(ctx, response.Body)
	for {
		line, err := reader.next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		parsed, err := parseGeminiWebLine(line)
		if err != nil {
			return err
		}
		if parsed == nil {
			continue
		}
		if parsed.RPCError != nil {
			return parsed.RPCError
		}
		if err := onLine(parsed); err != nil {
			return err
		}
	}
}

// geminiWebUpstreamStatus is a non-200 answer from the site, carrying the status
// the router has to see.
type geminiWebUpstreamStatus struct {
	Status    int
	Operation string
}

func (e *geminiWebUpstreamStatus) Error() string {
	return fmt.Sprintf("gemini-web: %s answered HTTP %d", e.Operation, e.Status)
}

// geminiWebRequestUUID mints the per-request uuid the turn echoes in two places.
func geminiWebRequestUUID() string {
	var raw [16]byte
	value := uint64(time.Now().UnixNano())
	for i := range raw {
		raw[i] = byte(value >> (8 * (i % 8)))
		value = value*6364136223846793005 + 1442695040888963407
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	hexDigits := "0123456789ABCDEF"
	out := make([]byte, 0, 36)
	for i, b := range raw {
		switch i {
		case 4, 6, 8, 10:
			out = append(out, '-')
		}
		out = append(out, hexDigits[b>>4], hexDigits[b&0x0f])
	}
	return string(out)
}

// errGeminiWebSessionRefused is the site's unauthenticated answer to a session
// that presented a credential it would not accept.
var errGeminiWebSessionRefused = errors.New(
	"gemini-web: gemini.google.com refused the configured session (code 1016) — " +
		"re-paste the __Secure-1PSID cookie from a signed-in gemini.google.com tab")
