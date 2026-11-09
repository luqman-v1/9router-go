package executor

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	json "encoding/json/v2"

	fhttp "github.com/bogdanfinn/fhttp"

	"9router/proxy/internal/providers"
)

// geminiWebFakeUpstream is a gemini.google.com stand-in for one turn.
//
// One handler serves every endpoint the executor calls — the bootstrap page, the
// model registry RPC and StreamGenerate — so a test exercises the real sequence
// rather than a stubbed seam. The frames are the ones a live session returned,
// so the parser is tested against the wire form it will actually meet.
type geminiWebFakeUpstream struct {
	mu sync.Mutex

	// calls records the path of every request served, in order.
	calls []string
	// headers captures the last request per path, so the shape sent upstream
	// can be asserted.
	headers map[string]http.Header
	// bodies captures the last request body per path.
	bodies map[string]string

	// bl and fSid are what the bootstrap page carries. Empty means the page
	// omits them, which is how a session the site will not establish is tested.
	bl   string
	fSid string
	// at is the SNlM0e anti-CSRF token. Empty is what an anonymous page carries:
	// the site issues it to a signed-in session only, so leaving it empty models
	// the anonymous case and setting it models a signed-in one.
	at string
	// registryBody is the otAQ7b answer. Empty means the default two-model list.
	registryBody string
	// registryStatus overrides the HTTP status the registry answers with.
	registryStatus int
	// streamBody is the raw StreamGenerate answer.
	streamBody string
	// streamStatus overrides the HTTP status StreamGenerate answers with.
	streamStatus int
	// rotateCookieTS, when set, is sent as a rotated __Secure-1PSIDTS on the
	// bootstrap page — the shape a live site uses to hand the session a fresh
	// value for a cookie the caller already presented.
	rotateCookieTS string
	// uploadPath is what the content-push service answers with. Empty makes
	// the upload fail, which is how a site that refuses the image is tested.
	uploadPath string
	// uploadStatus overrides the HTTP status the upload answers with.
	uploadStatus int
}

func newGeminiWebFakeUpstream(t *testing.T) *geminiWebFakeUpstream {
	t.Helper()
	return &geminiWebFakeUpstream{
		headers:        map[string]http.Header{},
		bodies:         map[string]string{},
		bl:             "boq_test-bl",
		fSid:           "1234567890",
		registryBody:   geminiWebRegistryLine("cf41b0e0dd7d53e5", "Flash-Lite", "3.5 Flash-Lite"),
		streamStatus:   http.StatusOK,
		registryStatus: http.StatusOK,
	}
}

// geminiWebRegistryLine renders one otAQ7b line in the wire shape the site uses:
// a wrb.fr envelope whose payload is a 19-slot array with the model list at 15
// and the tier flags at 16/17.
func geminiWebRegistryLine(id, category, displayName string) string {
	models := []any{[]any{id, category, "Fastest answers", []int{2, 140, 1}, nil, nil, nil, nil, nil, nil, nil, displayName, "Fastest answers"}}
	payload := make([]any, 19)
	payload[15] = models
	payload[16] = []int{9, 38}
	payload[17] = []int{2, 140, 115}
	encoded, _ := json.Marshal(payload)
	envelope, _ := json.Marshal([]any{[]any{"wrb.fr", "otAQ7b", string(encoded)}})
	return string(envelope)
}

func (f *geminiWebFakeUpstream) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.URL.Path)
	f.headers[r.URL.Path] = r.Header.Clone()
	var sb strings.Builder
	buffer := make([]byte, 32*1024)
	for {
		n, err := r.Body.Read(buffer)
		sb.Write(buffer[:n])
		if err != nil {
			break
		}
	}
	f.bodies[r.URL.Path] = sb.String()
}

func (f *geminiWebFakeUpstream) callPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *geminiWebFakeUpstream) header(path string) http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.headers[path]
}

func (f *geminiWebFakeUpstream) body(path string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[path]
}

func (f *geminiWebFakeUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.record(r)
	switch {
	case r.URL.Path == geminiWebUploadPath:
		// The content-push service answers with the path the site resolves
		// the attachment by.
		status := f.uploadStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if f.uploadPath != "" {
			w.Write([]byte(f.uploadPath))
		}
	case strings.Contains(r.URL.Path, "batchexecute"):
		w.WriteHeader(f.registryStatus)
		w.Write([]byte(")]}'\n\n" + f.registryBody + "\n"))
	case strings.Contains(r.URL.Path, "StreamGenerate"):
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(f.streamStatus)
		w.Write([]byte(f.streamBody))
	case r.URL.Path == "/app":
		page := "<html><script>window.WIZ_global_data = {"
		if f.bl != "" {
			page += `"cfb2h":"` + f.bl + `",`
		}
		if f.fSid != "" {
			page += `"FdrFJe":"` + f.fSid + `",`
		}
		if f.at != "" {
			page += `"SNlM0e":"` + f.at + `",`
		}
		page += `"Ylro7b":"pctx"}}</script></html>`
		if f.rotateCookieTS != "" {
			// No Domain: a cookie jar rejects a Domain that does not match the host
			// it was served from, and the fake runs on 127.0.0.1.
			http.SetCookie(w, &http.Cookie{Name: "__Secure-1PSIDTS", Value: f.rotateCookieTS, Path: "/"})
		}
		w.Write([]byte(page))
	default:
		http.NotFound(w, r)
	}
}

// geminiWebFrameLine renders one StreamGenerate frame in the real wire shape: a
// length line, then a wrb.fr envelope whose payload carries the answer *so far*
// at candidate[1][0], plus the continuation fields when they are set.
func geminiWebFrameLine(text string, done bool, servedModelID string) string {
	candidate := make([]any, 9)
	candidate[0] = "rc_test"
	candidate[1] = []any{text}
	if done {
		candidate[8] = []any{float64(2)}
	}
	payload := make([]any, 5)
	payload[1] = []any{"c_test", "r_test"}
	payload[4] = []any{candidate}
	if servedModelID != "" {
		// The tail fields collapse into the trailing sparse object, which is the
		// encoding a live payload uses for the served model identity.
		tail := map[string]any{"40": servedModelID, "26": "ctx-token"}
		payload = append(payload, tail)
	}
	encoded, _ := json.Marshal(payload)
	envelope, _ := json.Marshal([]any{[]any{"wrb.fr", nil, string(encoded)}})
	return "177\n" + string(envelope) + "\n"
}

// geminiWebStreamFixture is a whole answer as the site streams it: the answer
// grows across frames, the identity rides the first one, and `e` closes it.
func geminiWebStreamFixture(answer string) string {
	var sb strings.Builder
	sb.WriteString(")]}'\n\n")
	for i := 1; i <= len(answer); i++ {
		sb.WriteString(geminiWebFrameLine(answer[:i], i == len(answer), "cf41b0e0dd7d53e5"))
	}
	sb.WriteString(`[["e",11,null,null,22118]]` + "\n")
	return sb.String()
}

// withGeminiWebUpstream serves a fake site and returns its base URL.
//
// A session is keyed by credential and origin, and the anonymous credential is
// otherwise shared by every test in the process, so each test starts from a
// clean map.
func withGeminiWebUpstream(t *testing.T, upstream *geminiWebFakeUpstream) string {
	t.Helper()
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	geminiWebSessionMu.Lock()
	geminiWebSessions = map[string]*geminiWebSession{}
	geminiWebSessionMu.Unlock()
	return server.URL
}

// geminiWebTestConfig points a request at the fake upstream. The executor
// derives every endpoint from this base URL, which is what lets a whole turn —
// bootstrap page, registry RPC, stream — run against one httptest server.
func geminiWebTestConfig(baseURL string) *providers.ProviderConfig {
	return &providers.ProviderConfig{BaseURL: baseURL + "/app"}
}

func geminiWebTestBody(t *testing.T, model string, content string, extra map[string]any) []byte {
	t.Helper()
	body := map[string]any{
		"model":    model,
		"messages": []map[string]any{{"role": "user", "content": content}},
	}
	for key, value := range extra {
		body[key] = value
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal test body: %v", err)
	}
	return encoded
}

// streamedChunk mirrors a chunk as a client reads it.
type geminiWebChunk struct {
	Created int64 `json:"created"`
	Choices []struct {
		Delta struct {
			Role             string `json:"role"`
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

func readGeminiWebChunks(t *testing.T, body string) []geminiWebChunk {
	t.Helper()
	var chunks []geminiWebChunk
	for _, line := range strings.Split(body, "\n") {
		payload, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var chunk geminiWebChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}

func geminiWebChunkText(chunks []geminiWebChunk) string {
	var sb strings.Builder
	for _, chunk := range chunks {
		for _, choice := range chunk.Choices {
			sb.WriteString(choice.Delta.Content)
		}
	}
	return sb.String()
}

// TestGeminiWebStreamsAnAnonymousTurn is the whole path: bootstrap, model
// registry, then a streamed answer, with no credential at all.
//
// The site serves an anonymous visitor, which is the claim the executor is built
// on. The test pins the sequence the turn has to make and the answer a client
// receives, so a change that quietly starts requiring a cookie fails here.
func TestGeminiWebStreamsAnAnonymousTurn(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	upstream.streamBody = geminiWebStreamFixture("Hello there friend")
	server := withGeminiWebUpstream(t, upstream)

	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:      context.Background(),
		Config:   geminiWebTestConfig(server),
		Body:     geminiWebTestBody(t, "gemini-3.1-flash-lite", "hi", nil),
		IsStream: true,
	})
	if err != nil {
		t.Fatalf("ForwardGeminiWeb() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	chunks := readGeminiWebChunks(t, rec.Body.String())
	if got := geminiWebChunkText(chunks); got != "Hello there friend" {
		t.Errorf("streamed content = %q, want %q", got, "Hello there friend")
	}
	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Error("stream must be terminated with [DONE]")
	}
	// The answer must be sent once. The site repeats the whole answer on every
	// frame, so a delta bug shows up as the text being duplicated, not missing.
	// A chunk with no creation time is a shape some clients reject outright, so
	// the field has to be populated on every frame rather than left at its zero.
	if chunks[0].Created == 0 {
		t.Error("streamed chunks must carry a created timestamp")
	}
	if count := strings.Count(geminiWebChunkText(chunks), "friend"); count != 1 {
		t.Errorf("answer text appears %d times, want 1 (deltas must be derived, not forwarded)", count)
	}

	paths := upstream.callPaths()
	if len(paths) != 3 {
		t.Fatalf("upstream calls = %v, want bootstrap + registry + stream", paths)
	}
	if paths[0] != "/app" {
		t.Errorf("first call = %q, want the bootstrap page", paths[0])
	}
	if !strings.Contains(paths[1], "batchexecute") {
		t.Errorf("second call = %q, want the model registry RPC", paths[1])
	}
	if !strings.Contains(paths[2], "StreamGenerate") {
		t.Errorf("third call = %q, want StreamGenerate", paths[2])
	}

	// The session tokens the page carried have to reach StreamGenerate, or the
	// site answers the unauthenticated code instead of an answer.
	streamHeaders := upstream.header("/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate")
	if got := streamHeaders.Get("x-same-domain"); got != "1" {
		t.Errorf("x-same-domain = %q, want 1", got)
	}
	if !strings.Contains(streamHeaders.Get("cookie"), "NID") && streamHeaders.Get("cookie") != "" {
		t.Errorf("cookie = %q, want empty for an anonymous session", streamHeaders.Get("cookie"))
	}
	if header := streamHeaders.Get("x-goog-ext-525001261-jspb"); !strings.Contains(header, "cf41b0e0dd7d53e5") {
		t.Errorf("model header = %q, want the resolved model id in it", header)
	}
}

// TestGeminiWebStreamsTheAnswerIntoTheResponseBuffer proves a streamed turn is
// recorded for usage and for the request-details log.
//
// The buffer is where the usage recorder and the log read the answer from, so a
// streaming path that skips it files a request with an empty body and a zero
// token estimate — indistinguishable from the model having answered nothing.
func TestGeminiWebStreamsTheAnswerIntoTheResponseBuffer(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	upstream.streamBody = geminiWebStreamFixture("buffered for usage")
	server := withGeminiWebUpstream(t, upstream)

	var captured bytes.Buffer
	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:         context.Background(),
		Config:      geminiWebTestConfig(server),
		Body:        geminiWebTestBody(t, "gemini-3.1-flash-lite", "hi", nil),
		IsStream:    true,
		ResponseBuf: &captured,
	})
	if err != nil {
		t.Fatalf("ForwardGeminiWeb() error = %v", err)
	}
	if got := captured.String(); got != "buffered for usage" {
		t.Errorf("ResponseBuf = %q, want the streamed answer %q", got, "buffered for usage")
	}
}

// TestGeminiWebSendsTheBrowserHeaderSet pins the header shape the site's own
// client sends.
//
// These headers are colloquial to a browser and cheap to drop by accident: a
// refactor that "cleans up" the request would take the client hints with it, and
// the only symptom is a request that looks less like a browser than it should.
func TestGeminiWebSendsTheBrowserHeaderSet(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	upstream.streamBody = geminiWebStreamFixture("ok")
	server := withGeminiWebUpstream(t, upstream)

	rec := httptest.NewRecorder()
	if err := ForwardGeminiWeb(rec, &Request{
		Ctx:    context.Background(),
		Config: geminiWebTestConfig(server),
		Body:   geminiWebTestBody(t, "gemini-3.1-flash-lite", "hi", nil),
	}); err != nil {
		t.Fatalf("ForwardGeminiWeb() error = %v", err)
	}

	// The bootstrap page is a navigation, so it carries document hints.
	page := upstream.header("/app")
	for header, want := range map[string]string{
		"Sec-Fetch-Dest": "document",
		"Sec-Fetch-Mode": "navigate",
		"Sec-Ch-Ua":      "",
	} {
		got := page.Get(header)
		if want == "" {
			if !strings.Contains(got, "Chromium") {
				t.Errorf("bootstrap %s = %q, want a client-hint value naming the browser", header, got)
			}
			continue
		}
		if got != want {
			t.Errorf("bootstrap %s = %q, want %q", header, got, want)
		}
	}

	// The API calls are XHRs from the page, so they carry CORS hints instead.
	stream := upstream.header("/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate")
	for header, want := range map[string]string{
		"Sec-Fetch-Site": "same-origin",
		"Sec-Fetch-Mode": "cors",
		"Sec-Fetch-Dest": "empty",
		"Accept":         "*/*",
	} {
		if got := stream.Get(header); got != want {
			t.Errorf("stream %s = %q, want %q", header, got, want)
		}
	}
	if got := stream.Get("Referer"); !strings.HasSuffix(got, "/app") {
		t.Errorf("stream Referer = %q, want the page the call is issued from", got)
	}
}

// TestGeminiWebOrdersHeadersLikeChrome proves the ordering this provider imposes
// on a request is Chrome's, not Go's.
//
// The boundary is deliberate: this asserts the order *we* apply, through the
// HeaderOrderKey that fhttp honours on the wire. Asserting it after the request
// has been parsed is impossible — http.Header is a map and the order is gone —
// and asserting it on a raw socket would be testing fhttp rather than this code.
func TestGeminiWebOrdersHeadersLikeChrome(t *testing.T) {
	header := fhttp.Header{}
	values := geminiWebCORSValues("https://gemini.google.com", "https://gemini.google.com/app")
	values["content-type"] = "application/x-www-form-urlencoded;charset=utf-8"
	values["cookie"] = "NID=abc"
	values["x-goog-ext-525001261-jspb"] = "[1]"
	geminiWebSetOrdered(header, geminiWebCORSOrder, values)

	order := header[fhttp.HeaderOrderKey]
	if len(order) == 0 {
		t.Fatal("no header order recorded: the request would go out in Go's alphabetical order")
	}
	// Every name that carries a value must appear in the order, or fhttp writes it
	// in an unspecified position.
	for name, value := range values {
		if value == "" {
			continue
		}
		if !slices.Contains(order, name) {
			t.Errorf("%q carries a value but is missing from the header order: %v", name, order)
		}
	}
	position := func(name string) int { return slices.Index(order, name) }

	// The members Chrome orders specifically. A sorted header list fails these:
	// alphabetically "accept" is first and "x-same-domain" is last.
	if got := order[0]; got != "sec-ch-ua" {
		t.Errorf("first header = %q, want sec-ch-ua", got)
	}
	for _, pair := range [][2]string{
		{"sec-ch-ua", "x-same-domain"},
		{"x-same-domain", "user-agent"},
		{"user-agent", "content-type"},
		{"accept", "referer"},
		{"referer", "cookie"},
	} {
		first, second := position(pair[0]), position(pair[1])
		if first < 0 || second < 0 || first > second {
			t.Errorf("%s (%d) must be ordered before %s (%d); got %v", pair[0], first, pair[1], second, order)
		}
	}

	// A header the known order does not name must still be sent, or a
	// request-specific field would be silently dropped.
	values["x-unknown-field"] = "keep-me"
	header = fhttp.Header{}
	geminiWebSetOrdered(header, geminiWebCORSOrder, values)
	if !slices.Contains(header[fhttp.HeaderOrderKey], "x-unknown-field") {
		t.Error("an unrecognized header carrying a value must still be ordered and sent")
	}
	if got := header.Values("x-unknown-field"); len(got) != 1 || got[0] != "keep-me" {
		t.Errorf("unrecognized header value = %v, want [keep-me]", got)
	}

	// An empty value means "this request does not carry this", not "send it empty".
	header = fhttp.Header{}
	values["x-goog-ext-73010989-jspb"] = ""
	geminiWebSetOrdered(header, geminiWebCORSOrder, values)
	if got := header.Values("x-goog-ext-73010989-jspb"); len(got) != 0 {
		t.Errorf("an empty value must be omitted, got %v", got)
	}
}

// TestGeminiWebUsesChromePseudoHeaderOrder proves the HTTP/2 pseudo-header order
// is set, since that is part of what makes the connection look like a browser.
func TestGeminiWebUsesChromePseudoHeaderOrder(t *testing.T) {
	want := []string{":method", ":authority", ":scheme", ":path"}
	if !slices.Equal(geminiWebPseudoHeaderOrder, want) {
		t.Errorf("pseudo header order = %v, want %v", geminiWebPseudoHeaderOrder, want)
	}
}

// TestGeminiWebUsesTheStealthTLSStack proves the provider dials with the uTLS
// client rather than net/http, which is the whole point of the dependency.
func TestGeminiWebUsesTheStealthTLSStack(t *testing.T) {
	client, err := newGeminiWebStealthClient(nil, geminiWebOrigin, "")
	if err != nil {
		t.Fatalf("newGeminiWebStealthClient() error = %v", err)
	}
	if client == nil || client.client == nil {
		t.Fatal("the stealth client must be constructed, not nil")
	}
	if got := geminiWebTLSProfile.GetClientHelloStr(); !strings.Contains(strings.ToLower(got), "chrome") {
		t.Errorf("TLS profile is not a Chrome fingerprint: %q", got)
	}
}

// TestGeminiWebCarriesTheConnectionProxyIntoTheTLSStack proves an assigned proxy
// pool still applies after the move to the uTLS client.
//
// The chat path resolves the pool into the http.Client's transport before the
// executor runs. The uTLS client is a different stack and builds its own
// connection, so unless the proxy is read back off that transport the request
// would leave from the host IP while the dashboard still showed it as proxied —
// publishing the operator's egress address, which is the one thing a pool was
// assigned to prevent.
func TestGeminiWebCarriesTheConnectionProxyIntoTheTLSStack(t *testing.T) {
	proxyURL, err := url.Parse("http://proxy.example:8080")
	if err != nil {
		t.Fatalf("parse proxy url: %v", err)
	}
	reference := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	if got := geminiWebProxyFrom(reference); got != "http://proxy.example:8080" {
		t.Errorf("geminiWebProxyFrom() = %q, want the assigned proxy", got)
	}

	// A connection with no pool must yield no proxy, so the direct path is
	// unchanged rather than accidentally routed somewhere.
	direct := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	if got := geminiWebProxyFrom(&http.Client{}); got != "" {
		t.Errorf("geminiWebProxyFrom() = %q, want empty for a transport with no proxy", got)
	}
	_ = direct
}

// TestGeminiWebRefusesAModelTheSessionCannotServe proves a model the session does
// not offer is refused before the turn is sent.
//
// The site answers an unknown model with a different one and no error, so
// forward-and-hope returns Flash-Lite's answer under the Pro model's name. The
// only place to catch it is before the request, and the refusal has to name what
// is actually on offer or it is a dead end for the user.
func TestGeminiWebRefusesAModelTheSessionCannotServe(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	server := withGeminiWebUpstream(t, upstream)

	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:    context.Background(),
		Config: geminiWebTestConfig(server),
		Body:   geminiWebTestBody(t, "gemini-3.1-pro", "hi", nil),
	})
	if err == nil {
		t.Fatal("a model the session does not offer must be refused")
	}
	status, ok := deepSeekUpstreamStatus(err)
	if !ok || status != http.StatusBadRequest {
		t.Fatalf("error = %v (status %d), want 400", err, status)
	}
	if !strings.Contains(err.Error(), "Flash-Lite") {
		t.Errorf("refusal must name what the session does offer; got %v", err)
	}
	for _, path := range upstream.callPaths() {
		if strings.Contains(path, "StreamGenerate") {
			t.Fatalf("no turn should be sent for an unavailable model; calls = %v", upstream.callPaths())
		}
	}
}

// TestGeminiWebRefusesASubstitutedModel proves the identity check closes the case
// the pre-flight model check cannot: the session offered the model, then answered
// with another one.
func TestGeminiWebRefusesASubstitutedModel(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	// The registry offers Flash-Lite on a second id, and the answer comes back
	// stamped with it — a substitution under a resolved model's name.
	upstream.registryBody = geminiWebRegistryLine("cf41b0e0dd7d53e5", "Flash-Lite", "3.5 Flash-Lite")
	upstream.streamBody = geminiWebStreamFixture("substituted answer")
	upstream.streamBody = strings.ReplaceAll(upstream.streamBody, "cf41b0e0dd7d53e5", "aaaaaaaaaaaaaaaa")
	server := withGeminiWebUpstream(t, upstream)

	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:      context.Background(),
		Config:   geminiWebTestConfig(server),
		Body:     geminiWebTestBody(t, "gemini-3.1-flash-lite", "hi", nil),
		IsStream: true,
	})
	if err == nil {
		t.Fatal("a substituted model must be refused")
	}
	status, ok := deepSeekUpstreamStatus(err)
	if !ok || status != http.StatusBadGateway {
		t.Fatalf("error = %v (status %d), want 502", err, status)
	}
	if !strings.Contains(err.Error(), "answered with") {
		t.Errorf("refusal should name the substitution; got %v", err)
	}
}

// TestGeminiWebRefusalNamesTheCredentialThatWasTried pins the refusal's advice to
// the credential the session actually presented.
//
// Telling a caller "an anonymous session reaches one model" when a cookie is
// configured sends them looking for the cookie they already pasted, and the fix
// is the opposite one: replace it, rather than supply what is already there.
// Naming the wrong cause costs the support round trip the message exists to
// avoid, so every branch is pinned by the cause it names.
func TestGeminiWebRefusalNamesTheCredentialThatWasTried(t *testing.T) {
	tests := []struct {
		name string
		// apiKey is the credential the session presented.
		apiKey string
		// cause is the thing the advice has to name, and unwanted is the
		// thing it must not blame.
		cause    string
		unwanted string
	}{
		{
			name:     "no credential",
			apiKey:   "",
			cause:    "An anonymous session reaches one model",
			unwanted: "SAPISID",
		},
		{
			name:     "a credential without the session timestamp",
			apiKey:   "__Secure-1PSID=stale-cookie-value",
			cause:    "__Secure-1PSIDTS",
			unwanted: "An anonymous session",
		},
		{
			name:     "a complete credential",
			apiKey:   "__Secure-1PSID=psid-value; __Secure-1PSIDTS=sidts-value",
			cause:    "reaches this one model only",
			unwanted: "__Secure-1PSIDTS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := newGeminiWebFakeUpstream(t)
			server := withGeminiWebUpstream(t, upstream)

			rec := httptest.NewRecorder()
			err := ForwardGeminiWeb(rec, &Request{
				Ctx:    context.Background(),
				Config: geminiWebTestConfig(server),
				APIKey: tt.apiKey,
				Body:   geminiWebTestBody(t, "gemini-3.1-pro", "hi", nil),
			})
			if err == nil {
				t.Fatal("a model the session does not offer must be refused")
			}
			message := err.Error()
			if !strings.Contains(message, tt.cause) {
				t.Errorf("refusal = %q, want it to name %q", message, tt.cause)
			}
			if strings.Contains(message, tt.unwanted) {
				t.Errorf("refusal = %q, must not blame %q for this credential", message, tt.unwanted)
			}
			if !strings.Contains(message, "Flash-Lite") {
				t.Errorf("refusal must still name what the session offers; got %q", message)
			}
		})
	}
}

// TestGeminiWebTreatsTheRouterPlaceholderAsNoCredential proves the value the
// router substitutes for a credentialless provider is not sent to the site.
//
// A `NoAuth` provider is dispatched with its catalog `DefaultAPIKey`, so the
// executor receives the word "anonymous" (or "public") as `req.APIKey` even
// though nobody configured anything. Rendered as a cookie that becomes
// `__Secure-1PSID=anonymous` on every request — a credential the user never
// supplied, sent to a third party, and one that makes the session look
// credentialed so a refusal blames a cookie that does not exist.
func TestGeminiWebTreatsTheRouterPlaceholderAsNoCredential(t *testing.T) {
	for _, placeholder := range []string{"anonymous", "public"} {
		t.Run(placeholder, func(t *testing.T) {
			upstream := newGeminiWebFakeUpstream(t)
			upstream.streamBody = geminiWebStreamFixture("ok")
			server := withGeminiWebUpstream(t, upstream)

			rec := httptest.NewRecorder()
			err := ForwardGeminiWeb(rec, &Request{
				Ctx:      context.Background(),
				Config:   geminiWebTestConfig(server),
				APIKey:   placeholder,
				Body:     geminiWebTestBody(t, "gemini-3.1-flash-lite", "hi", nil),
				IsStream: true,
			})
			if err != nil {
				t.Fatalf("ForwardGeminiWeb() error = %v", err)
			}
			for _, path := range []string{"/app", "/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate"} {
				if got := upstream.header(path).Get("cookie"); strings.Contains(got, placeholder) {
					t.Errorf("cookie at %s = %q, must not carry the router placeholder", path, got)
				}
			}
		})
	}
}

// TestGeminiWebKeepsACredentialThatMerelyContainsAPlaceholderWord guards the
// other direction: the check must drop the exact placeholder and nothing else.
//
// A real cookie whose value happens to contain "anonymous" or "public" is a
// credential, and dropping it would silently turn a signed-in session into an
// anonymous one — the same class of bug, pointing the other way.
func TestGeminiWebKeepsACredentialThatMerelyContainsAPlaceholderWord(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	upstream.streamBody = geminiWebStreamFixture("ok")
	server := withGeminiWebUpstream(t, upstream)

	const cookie = "__Secure-1PSID=public-anonymous-session-value"
	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:      context.Background(),
		Config:   geminiWebTestConfig(server),
		APIKey:   cookie,
		Body:     geminiWebTestBody(t, "gemini-3.1-flash-lite", "hi", nil),
		IsStream: true,
	})
	if err != nil {
		t.Fatalf("ForwardGeminiWeb() error = %v", err)
	}
	if got := upstream.header("/app").Get("cookie"); !strings.Contains(got, cookie) {
		t.Errorf("cookie = %q, want the configured credential preserved", got)
	}
}

// TestGeminiWebSendsOneValuePerCookieAfterRotation proves a rotated cookie does
// not travel beside the stale value it replaces.
//
// The site rotates __Secure-1PSIDTS and returns the new value as Set-Cookie. Go's
// http.Client (and fhttp's, which this provider dials through) inserts jar cookies
// with AddCookie, so a Cookie header written by hand ends up carrying the value
// the caller pasted *and* the rotated one — the same name twice, the stale value
// first. Google reads a stale __Secure-1PSIDTS as a cancelled session, so the
// request is judged anonymous: it still succeeds, still returns a model list, and
// quietly offers one model instead of the account's several.
func TestGeminiWebSendsOneValuePerCookieAfterRotation(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	upstream.rotateCookieTS = "sidts-ROTATED-BY-THE-SITE"
	upstream.streamBody = geminiWebStreamFixture("ok")
	server := withGeminiWebUpstream(t, upstream)

	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:      context.Background(),
		Config:   geminiWebTestConfig(server),
		APIKey:   "__Secure-1PSIDTS=sidts-PASTED; __Secure-1PSID=psid-value",
		Body:     geminiWebTestBody(t, "gemini-3.1-flash-lite", "hi", nil),
		IsStream: true,
	})
	if err != nil {
		t.Fatalf("ForwardGeminiWeb() error = %v", err)
	}

	// The turn is the last call, so its cookie header reflects the rotation the
	// bootstrap page handed out.
	got := upstream.header("/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate").Get("cookie")
	if strings.Count(got, "__Secure-1PSIDTS=") > 1 {
		t.Errorf("cookie = %q, carries __Secure-1PSIDTS twice: the stale value and the rotated one", got)
	}
	if strings.Contains(got, "sidts-PASTED") {
		t.Errorf("cookie = %q, still carries the stale pasted value after rotation", got)
	}
	if !strings.Contains(got, "sidts-ROTATED-BY-THE-SITE") {
		t.Errorf("cookie = %q, must carry the rotated value the site issued", got)
	}
	if !strings.Contains(got, "__Secure-1PSID=psid-value") {
		t.Errorf("cookie = %q, must keep the cookies the site did not rotate", got)
	}
}

// TestGeminiWebPostsTheAntiCSRFTokenOfASignedInPage pins the `at` form field.
//
// The site's own JavaScript posts SNlM0e as `at` alongside `f.req`, and the
// reference implementation treats its absence on a signed-in page as an expired
// session. A session that has the token must therefore send it: the request a
// signed-in page makes is not the request an anonymous one makes, and sending
// the anonymous shape with a signed-in cookie is a mismatch between the
// credential and the call.
func TestGeminiWebPostsTheAntiCSRFTokenOfASignedInPage(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	upstream.at = "AOExyz-signed-in-token"
	upstream.streamBody = geminiWebStreamFixture("ok")
	server := withGeminiWebUpstream(t, upstream)

	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:      context.Background(),
		Config:   geminiWebTestConfig(server),
		APIKey:   "__Secure-1PSID=signed-in-cookie",
		Body:     geminiWebTestBody(t, "gemini-3.1-flash-lite", "hi", nil),
		IsStream: true,
	})
	if err != nil {
		t.Fatalf("ForwardGeminiWeb() error = %v", err)
	}

	for _, path := range []string{
		"/_/BardChatUi/data/batchexecute",
		"/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate",
	} {
		form, unescapeErr := url.QueryUnescape(upstream.body(path))
		if unescapeErr != nil {
			t.Fatalf("%s body is not a urlencoded form: %v", path, unescapeErr)
		}
		if !strings.Contains(form, "at="+upstream.at) {
			t.Errorf("%s body = %q, want the SNlM0e token posted as at", path, form)
		}
		// The value carries characters that must survive encoding: the site
		// answers a mis-encoded `at` with 400 rather than a readable error.
		if !strings.Contains(form, "f.req=") {
			t.Errorf("%s body = %q, want f.req alongside at", path, form)
		}
	}
}

// TestGeminiWebOmitsAtForAnAnonymousSession is the other half: an anonymous page
// carries no SNlM0e, so the request must not invent one.
func TestGeminiWebOmitsAtForAnAnonymousSession(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	upstream.streamBody = geminiWebStreamFixture("ok")
	server := withGeminiWebUpstream(t, upstream)

	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:      context.Background(),
		Config:   geminiWebTestConfig(server),
		Body:     geminiWebTestBody(t, "gemini-3.1-flash-lite", "hi", nil),
		IsStream: true,
	})
	if err != nil {
		t.Fatalf("ForwardGeminiWeb() error = %v", err)
	}
	form, unescapeErr := url.QueryUnescape(upstream.body("/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate"))
	if unescapeErr != nil {
		t.Fatalf("turn body is not a urlencoded form: %v", unescapeErr)
	}
	if strings.Contains(form, "at=") {
		t.Errorf("anonymous turn body = %q, must not carry an at token", form)
	}
}

// TestGeminiWebSignsTheRegistryCallForASignedInSession pins the `authorization`
// header on the model-registry RPC.
//
// That RPC is what answers "which models may this session use", so it is the call
// the site judges the caller on. A cookie presented without the signature can be
// answered as an anonymous visitor — the session still works, still returns a
// model list, and offers fewer models than the account has. Nothing downstream
// can tell that apart from an account that genuinely lacks them.
func TestGeminiWebSignsTheRegistryCallForASignedInSession(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	upstream.streamBody = geminiWebStreamFixture("ok")
	server := withGeminiWebUpstream(t, upstream)

	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:      context.Background(),
		Config:   geminiWebTestConfig(server),
		APIKey:   "SAPISID=sapisid-value; __Secure-1PSID=psid-value",
		Body:     geminiWebTestBody(t, "gemini-3.1-flash-lite", "hi", nil),
		IsStream: true,
	})
	if err != nil {
		t.Fatalf("ForwardGeminiWeb() error = %v", err)
	}
	got := upstream.header("/_/BardChatUi/data/batchexecute").Get("authorization")
	if !strings.HasPrefix(got, "SAPISIDHASH ") {
		t.Fatalf("authorization = %q, want a SAPISIDHASH value", got)
	}
	// The digest is over "<seconds> <SAPISID> <origin>", so verifying the shape
	// means recomputing it rather than trusting the prefix.
	parts := strings.SplitN(strings.TrimPrefix(got, "SAPISIDHASH "), "_", 2)
	if len(parts) != 2 {
		t.Fatalf("authorization = %q, want <seconds>_<hex>", got)
	}
	// The origin signed over is the one the session dials, which the test config
	// points at the fake upstream rather than at the real site.
	sum := sha1.Sum([]byte(parts[0] + " sapisid-value " + server))
	if want := hex.EncodeToString(sum[:]); parts[1] != want {
		t.Errorf("digest = %q, want %q over the SAPISID and origin", parts[1], want)
	}
}

// TestGeminiWebDoesNotSignAnAnonymousCall is the other half: with nothing to sign
// there must be no header, because a malformed one is worse than none.
func TestGeminiWebDoesNotSignAnAnonymousCall(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	upstream.streamBody = geminiWebStreamFixture("ok")
	server := withGeminiWebUpstream(t, upstream)

	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:      context.Background(),
		Config:   geminiWebTestConfig(server),
		Body:     geminiWebTestBody(t, "gemini-3.1-flash-lite", "hi", nil),
		IsStream: true,
	})
	if err != nil {
		t.Fatalf("ForwardGeminiWeb() error = %v", err)
	}
	if got := upstream.header("/_/BardChatUi/data/batchexecute").Get("authorization"); got != "" {
		t.Errorf("authorization = %q, want empty for a session with no SAPISID", got)
	}
}

// TestGeminiWebDoesNotSignWithAPrefixOfAnotherCookieName guards the cookie lookup.
//
// __Secure-1PSID is a prefix of __Secure-1PSIDTS, and signing with the wrong one
// produces a header the site rejects without a diagnostic, so the match has to be
// on the whole name.
func TestGeminiWebDoesNotSignWithAPrefixOfAnotherCookieName(t *testing.T) {
	// A credential carrying __Secure-1PSIDTS but no SAPISID has nothing to sign.
	if got := geminiWebSAPISIDHash("__Secure-1PSIDTS=ts-value; __Secure-1PSID=psid-value", "https://gemini.google.com"); got != "" {
		t.Errorf("hash = %q, want empty: the credential carries no SAPISID", got)
	}
	// And the real SAPISID is picked up when it is present.
	if got := geminiWebSAPISIDHash("SAPISID=real-value", "https://gemini.google.com"); !strings.HasPrefix(got, "SAPISIDHASH ") {
		t.Errorf("hash = %q, want a SAPISIDHASH value", got)
	}
}

// TestGeminiWebReportsAnExpiredSessionAsUnauthorized proves a refused credential
// surfaces as 401, the one status the router rotates a connection on.
func TestGeminiWebReportsAnExpiredSessionAsUnauthorized(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	// The site's unauthenticated answer: HTTP 200 with code 1016 in the payload.
	upstream.registryBody = `[["wrb.fr","otAQ7b","[null,false,false,1016]"]]`
	server := withGeminiWebUpstream(t, upstream)

	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:    context.Background(),
		Config: geminiWebTestConfig(server),
		APIKey: "__Secure-1PSID=stale-cookie",
		Body:   geminiWebTestBody(t, "gemini-3.1-flash-lite", "hi", nil),
	})
	if err == nil {
		t.Fatal("a refused session must not be reported as success")
	}
	status, ok := deepSeekUpstreamStatus(err)
	if !ok || status != http.StatusUnauthorized {
		t.Fatalf("error = %v (status %d), want 401", err, status)
	}
	if !strings.Contains(err.Error(), "__Secure-1PSID") {
		t.Errorf("the refusal must say what to paste; got %v", err)
	}
}

// TestGeminiWebReportsARejectedCookieHeader proves the cookie reaches upstream.
//
// The credential is the only thing that changes which models a session can be
// served, so a cookie that never left the process would look exactly like an
// anonymous session — and every model choice would be refused for no visible
// reason.
func TestGeminiWebReportsARejectedCookieHeader(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	upstream.streamBody = geminiWebStreamFixture("ok")
	server := withGeminiWebUpstream(t, upstream)

	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:      context.Background(),
		Config:   geminiWebTestConfig(server),
		APIKey:   "__Secure-1PSID=cookie-value",
		Body:     geminiWebTestBody(t, "gemini-3.1-flash-lite", "hi", nil),
		IsStream: true,
	})
	if err != nil {
		t.Fatalf("ForwardGeminiWeb() error = %v", err)
	}
	if got := upstream.header("/app").Get("cookie"); !strings.Contains(got, "__Secure-1PSID=cookie-value") {
		t.Errorf("bootstrap cookie = %q, want the configured credential", got)
	}
	stream := upstream.header("/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate")
	if got := stream.Get("cookie"); !strings.Contains(got, "__Secure-1PSID=cookie-value") {
		t.Errorf("stream cookie = %q, want the configured credential", got)
	}
}

// TestGeminiWebReportsAFailedTurnAsUpstreamFailure proves a truncated stream is a
// failure rather than a short answer.
//
// A stream that ends without ever carrying text is indistinguishable from a
// completed turn with an empty answer unless it is checked, and reporting it as
// success would clear the account's cooldown on a turn that produced nothing.
func TestGeminiWebReportsAFailedTurnAsUpstreamFailure(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	upstream.streamBody = ")]}'\n\n177\n" // framing only, no frame ever arrives
	server := withGeminiWebUpstream(t, upstream)

	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:    context.Background(),
		Config: geminiWebTestConfig(server),
		Body:   geminiWebTestBody(t, "gemini-3.1-flash-lite", "hi", nil),
	})
	if err == nil {
		t.Fatal("a stream with no answer must not be reported as success")
	}
	status, ok := deepSeekUpstreamStatus(err)
	if !ok || status != http.StatusBadGateway {
		t.Fatalf("error = %v (status %d), want 502", err, status)
	}
}

// TestGeminiWebReportsAMissingBootstrapToken proves a page without its session
// tokens fails loudly rather than sending a turn the site will refuse.
func TestGeminiWebReportsAMissingBootstrapToken(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	upstream.bl = ""
	upstream.fSid = ""
	server := withGeminiWebUpstream(t, upstream)

	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:    context.Background(),
		Config: geminiWebTestConfig(server),
		Body:   geminiWebTestBody(t, "gemini-3.1-flash-lite", "hi", nil),
	})
	if err == nil {
		t.Fatal("a bootstrap page with no session tokens must fail")
	}
	if !strings.Contains(err.Error(), "session tokens") {
		t.Errorf("error should name what was missing; got %v", err)
	}
}

// TestGeminiWebWritesANonStreamingTurn proves the buffered shape a client that
// did not ask to stream receives.
func TestGeminiWebWritesANonStreamingTurn(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	upstream.streamBody = geminiWebStreamFixture("buffered answer")
	server := withGeminiWebUpstream(t, upstream)

	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:    context.Background(),
		Config: geminiWebTestConfig(server),
		Body:   geminiWebTestBody(t, "gemini-3.1-flash-lite", "hi", nil),
	})
	if err != nil {
		t.Fatalf("ForwardGeminiWeb() error = %v", err)
	}
	var out struct {
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not a chat.completion: %v (body %s)", err, rec.Body.String())
	}
	if out.Object != "chat.completion" {
		t.Errorf("object = %q, want chat.completion", out.Object)
	}
	if len(out.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(out.Choices))
	}
	if out.Choices[0].Message.Content != "buffered answer" {
		t.Errorf("content = %q, want %q", out.Choices[0].Message.Content, "buffered answer")
	}
	if out.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", out.Choices[0].FinishReason)
	}
}

// TestGeminiWebReportsTheReasoningChannel proves the model's thinking reaches
// reasoning_content rather than being dropped or merged into the answer.
func TestGeminiWebReportsTheReasoningChannel(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	upstream.streamBody = geminiWebReasoningFixture("12", "the answer is twelve")
	server := withGeminiWebUpstream(t, upstream)

	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:      context.Background(),
		Config:   geminiWebTestConfig(server),
		Body:     geminiWebTestBody(t, "gemini-3.1-flash-lite", "12?", map[string]any{"reasoning_effort": "high"}),
		IsStream: true,
	})
	if err != nil {
		t.Fatalf("ForwardGeminiWeb() error = %v", err)
	}
	var reasoning strings.Builder
	for _, chunk := range readGeminiWebChunks(t, rec.Body.String()) {
		for _, choice := range chunk.Choices {
			reasoning.WriteString(choice.Delta.ReasoningContent)
		}
	}
	if got := reasoning.String(); got != "12" {
		t.Errorf("reasoning_content = %q, want %q", got, "12")
	}
	if got := geminiWebChunkText(readGeminiWebChunks(t, rec.Body.String())); got != "the answer is twelve" {
		t.Errorf("content = %q, want the answer alone", got)
	}
}

// geminiWebReasoningFixture streams an answer with a reasoning channel, which
// the site sends at candidate[37].
func geminiWebReasoningFixture(thoughts, answer string) string {
	var sb strings.Builder
	sb.WriteString(")]}'\n\n")
	for i := 1; i <= len(answer); i++ {
		candidate := make([]any, 38)
		candidate[0] = "rc_test"
		candidate[1] = []any{answer[:i]}
		candidate[37] = []any{[]any{thoughts}}
		if i == len(answer) {
			candidate[8] = []any{float64(2)}
		}
		payload := make([]any, 5)
		payload[1] = []any{"c_test", "r_test"}
		payload[4] = []any{candidate}
		payload = append(payload, map[string]any{"40": "cf41b0e0dd7d53e5", "26": "ctx"})
		encoded, _ := json.Marshal(payload)
		envelope, _ := json.Marshal([]any{[]any{"wrb.fr", nil, string(encoded)}})
		sb.WriteString("177\n" + string(envelope) + "\n")
	}
	sb.WriteString(`[["e",11,null,null,22118]]` + "\n")
	return sb.String()
}

// TestGeminiWebStreamsToolCalls proves a recovered tool call reaches a streaming
// client as tool_calls, with the arguments whole and the call indexed.
//
// This site has no native tool channel, so a call only exists once the answer is
// whole and is recovered by parsing it — which is why a turn that declared tools
// buffers instead of streaming text through.
func TestGeminiWebStreamsToolCalls(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	answer := `{"name": "get_weather", "arguments": {"city": "Bandung"}}`
	upstream.streamBody = geminiWebStreamFixture(answer)
	server := withGeminiWebUpstream(t, upstream)

	body := map[string]any{
		"model":    "gemini-3.1-flash-lite",
		"messages": []map[string]any{{"role": "user", "content": "weather in Bandung?"}},
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "get_weather",
				"description": "Look up the weather",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
			},
		}},
	}
	encoded, _ := json.Marshal(body)

	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:      context.Background(),
		Config:   geminiWebTestConfig(server),
		Body:     encoded,
		IsStream: true,
	})
	if err != nil {
		t.Fatalf("ForwardGeminiWeb() error = %v", err)
	}

	var calls []struct {
		ID    string
		Name  string
		Args  string
		Index int
	}
	for _, chunk := range readGeminiWebChunks(t, rec.Body.String()) {
		for _, choice := range chunk.Choices {
			for _, call := range choice.Delta.ToolCalls {
				calls = append(calls, struct {
					ID    string
					Name  string
					Args  string
					Index int
				}{call.ID, call.Function.Name, call.Function.Arguments, call.Index})
			}
		}
	}
	if len(calls) != 1 {
		t.Fatalf("tool_calls = %d, want 1 (body %s)", len(calls), rec.Body.String())
	}
	if calls[0].Name != "get_weather" {
		t.Errorf("tool name = %q, want get_weather", calls[0].Name)
	}
	if calls[0].ID == "" {
		t.Error("tool call must carry an id for the client to answer it with")
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(calls[0].Args), &args); err != nil {
		t.Fatalf("tool arguments are not JSON: %q (%v)", calls[0].Args, err)
	}
	if args["city"] != "Bandung" {
		t.Errorf("tool arguments = %v, want city=Bandung", args)
	}
	// The raw JSON object must not also arrive as assistant text, or the client
	// shows the invocation to the user as prose.
	if text := strings.TrimSpace(geminiWebChunkText(readGeminiWebChunks(t, rec.Body.String()))); strings.Contains(text, "get_weather") {
		t.Errorf("content = %q, want the invocation removed", text)
	}
}

// TestGeminiWebSendsTheDeclaredToolsInThePrompt proves the tool declarations
// reach the site, which has no field for them.
func TestGeminiWebSendsTheDeclaredToolsInThePrompt(t *testing.T) {
	upstream := newGeminiWebFakeUpstream(t)
	upstream.streamBody = geminiWebStreamFixture("ok")
	server := withGeminiWebUpstream(t, upstream)

	body := map[string]any{
		"model":    "gemini-3.1-flash-lite",
		"messages": []map[string]any{{"role": "user", "content": "weather?"}},
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "get_weather",
				"description": "Look up the weather",
				"parameters":  map[string]any{"type": "object"},
			},
		}},
	}
	encoded, _ := json.Marshal(body)

	rec := httptest.NewRecorder()
	if err := ForwardGeminiWeb(rec, &Request{
		Ctx:    context.Background(),
		Config: geminiWebTestConfig(server),
		Body:   encoded,
	}); err != nil {
		t.Fatalf("ForwardGeminiWeb() error = %v", err)
	}

	// The turn body is a urlencoded form, so it is decoded before being read as
	// the request array the site parses.
	turnBody := upstream.body("/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate")
	prompt, unescapeErr := url.QueryUnescape(turnBody)
	if unescapeErr != nil {
		t.Fatalf("turn body is not a urlencoded form: %v", unescapeErr)
	}
	if !strings.Contains(prompt, "get_weather") {
		t.Error("tool declarations must reach the site, which has no field for them")
	}
	if !strings.Contains(prompt, "Look up the weather") {
		t.Error("the tool description must reach the site")
	}
}

var _ = time.Second

// geminiWebFullRegistry is the model set a signed-in account reports: the
// Flash-Lite an anonymous session is limited to, plus Flash, Pro and Argon.
func geminiWebFullRegistry() []geminiWebModel {
	return []geminiWebModel{
		{ID: "cf41b0e0dd7d53e5", Category: "Flash-Lite", DisplayName: "3.5 Flash-Lite"},
		{ID: "9d1f8b7e6a5c4321", Category: "Flash", DisplayName: "3.1 Flash"},
		{ID: "95a4c0abcba24364", Category: "Pro", DisplayName: "3.1 Pro"},
		{ID: "77b2d1e9f0a34567", Category: "Argon", DisplayName: "4 Argon"},
	}
}

// TestGeminiWebResolvesBothRegistryNamingForms proves every name the catalogue
// publishes reaches the registry entry it names.
//
// The site's registry names a model twice: by category — "Pro", "Argon",
// "Flash-Lite" — and by a display label carrying the version, "3.1 Pro",
// "4 Argon". A client written against the site's own naming asks for
// `gemini-4-argon`; one reading this catalogue asks for `gemini-argon`. Both
// have to land on the same entry, and no form may be matched by a different
// model's slug: `gemini-pro` picking the Flash-Lite entry would answer a Pro
// request with the smallest model on offer and report success.
func TestGeminiWebResolvesBothRegistryNamingForms(t *testing.T) {
	registry := geminiWebFullRegistry()

	tests := []struct {
		requested string
		wantID    string
	}{
		{"gemini-argon", "77b2d1e9f0a34567"},
		{"gemini-4-argon", "77b2d1e9f0a34567"},
		{"gemini-flash-lite", "cf41b0e0dd7d53e5"},
		{"gemini-3.5-flash-lite", "cf41b0e0dd7d53e5"},
		{"gemini-3.1-flash-lite", "cf41b0e0dd7d53e5"},
		{"gemini-flash", "9d1f8b7e6a5c4321"},
		{"gemini-3.7-flash", "9d1f8b7e6a5c4321"},
		{"gemini-pro", "95a4c0abcba24364"},
		{"gemini-3.1-pro", "95a4c0abcba24364"},
		{"gemini-3-pro", "95a4c0abcba24364"},
	}

	for _, tt := range tests {
		t.Run(tt.requested, func(t *testing.T) {
			got := geminiWebResolveModel(registry, tt.requested)
			if got == nil {
				t.Fatalf("%q is catalogued but resolved to no registry entry", tt.requested)
			}
			if got.ID != tt.wantID {
				t.Errorf("%q resolved to %s (%q), want %q", tt.requested, got.Category, got.ID, tt.wantID)
			}
		})
	}
}

// TestGeminiWebCatalogueNamesAllResolve stops a catalogued name from being dead
// on arrival.
// A name added to the catalogue but never matched by the registry parser reaches
// the user as "cannot be served by this session" even on an account that has the
// model — the provider looks broken for that model rather than mislabelled. Every
// catalogue entry is therefore resolved against the fullest registry the site is
// known to report, so adding a name without teaching the matcher about it fails
// here instead of in a user's session.
func TestGeminiWebCatalogueNamesAllResolve(t *testing.T) {
	registry := geminiWebFullRegistry()

	catalogued := providers.GetProviderModels("gemini-web")
	if len(catalogued) == 0 {
		t.Fatal("gemini-web publishes no models")
	}
	for _, name := range catalogued {
		if geminiWebResolveModel(registry, name) == nil {
			t.Errorf("catalogued model %q matches no registry entry, so it can never be served", name)
		}
	}
}

// TestGeminiWebArgonCarriesItsThinkingLevel proves the Argon model takes a
// reasoning level like every other one.
// The site's enum is not ordered the way the words are — low is 1, medium is 4,
// high is 2 — so a model wired to the wrong level would look like it answers
// while quietly doing so at the floor.
func TestGeminiWebArgonCarriesItsThinkingLevel(t *testing.T) {
	tests := []struct {
		effort string
		want   int
	}{
		{"low", 1},
		{"medium", 4},
		{"high", 2},
	}
	for _, tt := range tests {
		t.Run(tt.effort, func(t *testing.T) {
			body := geminiWebRequestEnvelope{Model: "gemini-4-argon", ReasoningEffort: tt.effort}
			if got := geminiWebThinkingLevel(body, nil); got != tt.want {
				t.Errorf("effort %q mapped to %d, want %d", tt.effort, got, tt.want)
			}
		})
	}
}
