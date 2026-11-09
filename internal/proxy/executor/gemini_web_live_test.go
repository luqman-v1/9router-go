package executor

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/providers"
)

// Live tests reach the real gemini.google.com.
//
// The site is a third party, so the suite is opt-in: it is skipped unless
// 9ROUTER_LIVE_TESTS=1 is set, and CI does not set it. The anonymous turn
// needs no credential; the signed-in ones read one from the environment so it
// never reaches the repository.
//
// Run them with:
//
//	9ROUTER_LIVE_TESTS=1 go test ./internal/proxy/executor/ -run TestGeminiWebLive -v
//
// The signed-in ones also need:
//
//	9ROUTER_LIVE_GEMINI_WEB_COOKIE='__Secure-1PSID=…; SAPISID=…; __Secure-1PSIDTS=…'

// liveUpstreamEnv is the opt-in variable. The 9ROUTER_ prefix keeps it from
// colliding with a real runtime setting and makes it greppable.
const liveUpstreamEnv = "9ROUTER_LIVE_TESTS"

// liveCookieEnv names the signed-in credential the live tests use.
const liveCookieEnv = "9ROUTER_LIVE_GEMINI_WEB_COOKIE"

// liveUpstreamEnabled reports whether live upstream tests were opted into.
//
// Only an explicit opt-in counts, and "1" is the spelling the Makefile uses.
// A developer who writes 9ROUTER_LIVE_TESTS=true must not silently get a
// skipped run, so the accepted spellings are exactly the ones the chat
// package's own live guard accepts.
func liveUpstreamEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(liveUpstreamEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// requireLiveUpstream skips the calling test unless live tests were enabled.
func requireLiveUpstream(t *testing.T) {
	t.Helper()
	if !liveUpstreamEnabled() {
		t.Skipf("live upstream test: set %s=1 to run it against gemini.google.com", liveUpstreamEnv)
	}
}

// requireLiveCookie returns the signed-in credential the live tests use.
//
// A credential without __Secure-1PSIDTS is refused rather than used: that
// cookie is what carries the session into the model registry, so a header
// without it is answered as an anonymous visitor's and a run that passed
// would prove nothing about a signed-in session.
func requireLiveCookie(t *testing.T) string {
	t.Helper()
	cookie := strings.TrimSpace(os.Getenv(liveCookieEnv))
	if cookie == "" {
		t.Skipf("live upstream test: set %s to a signed-in cookie header", liveCookieEnv)
	}
	if !GeminiWebHasSessionTimestamp(cookie) {
		t.Fatalf("the credential carries no __Secure-1PSIDTS, so gemini.google.com answers it as an anonymous visitor")
	}
	return cookie
}

// TestGeminiWebLiveAnonymousTurn exercises a turn with no credential.
func TestGeminiWebLiveAnonymousTurn(t *testing.T) {
	requireLiveUpstream(t)
	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:      context.Background(),
		Config:   &providers.ProviderConfig{},
		Body:     geminiWebTestBody(t, "gemini-3.1-flash-lite", "Reply with the word pong and nothing else.", nil),
		IsStream: true,
	})
	if err != nil {
		t.Fatalf("ForwardGeminiWeb() error = %v", err)
	}
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "pong") {
		t.Errorf("the answer carries no pong:\n%s", body)
	}
	if !strings.Contains(body, "\"usage\"") {
		t.Errorf("the stream carries no usage frame:\n%s", body)
	}
}

// TestGeminiWebLiveSignedInTurn asks the site which models the account may be
// served.
//
// This is the check that proves the credential reaches the signed-in models
// rather than one model's worth of anonymous access: the model registry is
// answered as a visitor unless the call is signed.
func TestGeminiWebLiveSignedInTurn(t *testing.T) {
	requireLiveUpstream(t)
	cookie := requireLiveCookie(t)

	session := geminiWebSessionForCredential(cookie)
	models, err := session.availableModels(context.Background())
	if err != nil {
		t.Fatalf("availableModels() error = %v", err)
	}
	names := make([]string, 0, len(models))
	for _, model := range models {
		names = append(names, model.Category)
	}
	t.Logf("the session may be served: %s", strings.Join(names, ", "))
	if len(models) < 2 {
		t.Errorf("the session reaches %d model(s); a signed-in account is expected to reach more than one",
			len(models))
	}
}

// TestGeminiWebLiveSignedInTurnWithAnImage exercises a turn that carries an
// image.
//
// The image is the part that needed the session cookie: the upload goes to
// Google's content-push service on another host, and without the cookie the
// upload belongs to no session. The picture is a solid red square and the
// answer has to name the colour, so a turn that never saw the picture cannot
// pass — an answer about the colour is the proof that the attachment was
// resolved and handed to the model.
func TestGeminiWebLiveSignedInTurnWithAnImage(t *testing.T) {
	requireLiveUpstream(t)
	cookie := requireLiveCookie(t)

	// A 32x32 solid red PNG, so the test needs no file and no network of its
	// own. The pixels are one colour so the answer is not a matter of taste.
	png := solidPNGBase64(32, 32, 0xff, 0x00, 0x00)

	rec := httptest.NewRecorder()
	err := ForwardGeminiWeb(rec, &Request{
		Ctx:    context.Background(),
		Config: &providers.ProviderConfig{},
		APIKey: cookie,
		Body: geminiWebTestBody(t, "gemini-3.1-flash-lite", "", map[string]any{
			"messages": []map[string]any{{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": "What colour fills this image? Answer with one word."},
					{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + png}},
				},
			}},
		}),
		IsStream: true,
	})
	if err != nil {
		t.Fatalf("ForwardGeminiWeb() error = %v", err)
	}
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	answer := geminiWebAnswerText(body)
	if answer == "" {
		t.Fatalf("the stream carries no answer:\n%s", body)
	}
	t.Logf("the answer: %s", answer)
	if !strings.Contains(strings.ToLower(answer), "red") {
		t.Errorf("the answer = %q, want it to name the colour of the picture", answer)
	}
}

// TestGeminiWebLiveSignedInToolCall exercises a tool call and the round trip
// that follows it.
//
// The site has no tool channel, so the model writes the call as text and this
// provider reads it back. The first turn has to produce a call the client can
// run, and the second has to use the result the client sends back — a
// provider that lost either half would break every agent built on it.
func TestGeminiWebLiveSignedInToolCall(t *testing.T) {
	requireLiveUpstream(t)
	cookie := requireLiveCookie(t)

	tools := []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name":        "get_weather",
			"description": "Get the current weather for a city",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"city": map[string]any{"type": "string"}},
				"required":   []string{"city"},
			},
		},
	}}
	question := "What is the weather in Jakarta right now? Use the get_weather tool."

	first, err := json.Marshal(map[string]any{
		"model":    "gemini-3.1-pro",
		"messages": []map[string]any{{"role": "user", "content": question}},
		"tools":    tools,
	})
	if err != nil {
		t.Fatalf("marshal the first turn: %v", err)
	}
	rec := httptest.NewRecorder()
	if err := ForwardGeminiWeb(rec, &Request{
		Ctx:    context.Background(),
		Config: &providers.ProviderConfig{},
		APIKey: cookie,
		Body:   first,
	}); err != nil {
		t.Fatalf("ForwardGeminiWeb() error = %v", err)
	}
	body := rec.Body.String()
	t.Logf("the first turn: %s", body)
	if !strings.Contains(body, "\"tool_calls\"") {
		t.Fatalf("the first turn carries no tool call:\n%s", body)
	}
	if !strings.Contains(body, "get_weather") {
		t.Errorf("the tool call names no declared tool:\n%s", body)
	}

	second, err := json.Marshal(map[string]any{
		"model": "gemini-3.1-pro",
		"messages": []map[string]any{
			{"role": "user", "content": question},
			{"role": "assistant", "content": "", "tool_calls": []map[string]any{{
				"id":       "call_get_weather_1",
				"type":     "function",
				"function": map[string]any{"name": "get_weather", "arguments": `{"city": "Jakarta"}`},
			}}},
			{"role": "tool", "tool_call_id": "call_get_weather_1", "name": "get_weather",
				"content": `{"tempC":31,"condition":"sunny"}`},
		},
		"tools": tools,
	})
	if err != nil {
		t.Fatalf("marshal the second turn: %v", err)
	}
	rec2 := httptest.NewRecorder()
	if err := ForwardGeminiWeb(rec2, &Request{
		Ctx:    context.Background(),
		Config: &providers.ProviderConfig{},
		APIKey: cookie,
		Body:   second,
	}); err != nil {
		t.Fatalf("ForwardGeminiWeb() error = %v", err)
	}
	answer := rec2.Body.String()
	t.Logf("the second turn: %s", answer)
	if !strings.Contains(answer, "sunny") && !strings.Contains(answer, "31") {
		t.Errorf("the second turn does not use the tool result:\n%s", answer)
	}
}

// TestGeminiWebLiveRotation renews the session against the live endpoint and
// uses the renewed credential.
//
// This is the check that the renewal is a renewal and not a no-op: the site
// answers with a new cookie, the renewed credential still reaches the
// account's models, and a turn on it still answers.
func TestGeminiWebLiveRotation(t *testing.T) {
	requireLiveUpstream(t)
	cookie := requireLiveCookie(t)

	session := geminiWebSessionForCredential(cookie)
	result := geminiWebRotate(context.Background(), session, cookie)
	t.Logf("the renewal: status=%d renewed=%v next=%v", result.Status, result.Rotated, result.NextInterval)
	if !result.Rotated {
		t.Fatalf("the renewal issued no cookie (status %d)", result.Status)
	}
	if result.Cookie == cookie {
		t.Fatal("the renewal returned the credential it was given")
	}

	renewed := geminiWebSessionForCredential(result.Cookie)
	models, err := renewed.availableModels(context.Background())
	if err != nil {
		t.Fatalf("availableModels() after the renewal: %v", err)
	}
	names := make([]string, 0, len(models))
	for _, model := range models {
		names = append(names, model.Category)
	}
	t.Logf("after the renewal the session may be served: %s", strings.Join(names, ", "))
	if len(models) < 2 {
		t.Errorf("the renewed credential reaches %d model(s)", len(models))
	}
}

// TestGeminiWebLiveRotationLoop renews the session several times in a row.
//
// One renewal proves that the endpoint answers. What the background worker
// actually does is renew again and again, so the things that can only show up
// over several renewals are the ones worth pinning:
//
//   - the site answers a second renewal inside the same minute with HTTP 429,
//     so a worker that renewed too often would lock itself out;
//   - each renewed credential has to keep reaching the account's models, or
//     the renewal would quietly cost the account its paid models.
//
// The gap between the renewals is the site's own minute, so a run takes a
// few minutes. It is a live test, so it only runs when one is asked for.
func TestGeminiWebLiveRotationLoop(t *testing.T) {
	requireLiveUpstream(t)
	cookie := requireLiveCookie(t)

	const renewals = 3
	credential := cookie
	for attempt := 1; attempt <= renewals; attempt++ {
		session := geminiWebSessionForCredential(credential)
		result := geminiWebRotate(context.Background(), session, credential)
		t.Logf("renewal %d: status=%d renewed=%v next=%v",
			attempt, result.Status, result.Rotated, result.NextInterval)
		if !result.Rotated {
			t.Fatalf("renewal %d issued no cookie (status %d)", attempt, result.Status)
		}
		if result.Status != http.StatusOK {
			t.Fatalf("renewal %d was refused (status %d)", attempt, result.Status)
		}
		credential = result.Cookie

		renewed := geminiWebSessionForCredential(credential)
		models, err := renewed.availableModels(context.Background())
		if err != nil {
			t.Fatalf("renewal %d: the renewed credential is refused: %v", attempt, err)
		}
		names := make([]string, 0, len(models))
		for _, model := range models {
			names = append(names, model.Category)
		}
		t.Logf("renewal %d leaves the session able to be served: %s", attempt, strings.Join(names, ", "))
		if len(models) < 2 {
			t.Fatalf("renewal %d left the session reaching %d model(s)", attempt, len(models))
		}

		if attempt < renewals {
			// The site's own minute. A renewal inside it is answered with
			// HTTP 429, which is what the gap the worker uses is there to
			// avoid.
			wait := geminiWebRotateMinInterval + 5*time.Second
			t.Logf("waiting %v before renewal %d", wait, attempt+1)
			time.Sleep(wait)
		}
	}
}

// geminiWebAnswerText reads the answer out of a streamed chat completion.
func geminiWebAnswerText(body string) string {
	var out strings.Builder
	for line := range strings.Lines(body) {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(trimmed, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var chunk streamedChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		for _, choice := range chunk.Choices {
			if text, ok := choice.Delta["content"].(string); ok {
				out.WriteString(text)
			}
		}
	}
	return strings.TrimSpace(out.String())
}

// solidPNGBase64 renders a solid-colour PNG.
func solidPNGBase64(width, height int, r, g, b byte) string {
	var raw bytes.Buffer
	for range height {
		raw.WriteByte(0) // filter: none
		for range width {
			raw.Write([]byte{r, g, b})
		}
	}
	var out bytes.Buffer
	out.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	chunk := func(kind string, payload []byte) {
		var length bytes.Buffer
		_ = binary.Write(&length, binary.BigEndian, uint32(len(payload)))
		out.Write(length.Bytes())
		body := append([]byte(kind), payload...)
		sum := crc32.ChecksumIEEE(body)
		var checksum bytes.Buffer
		_ = binary.Write(&checksum, binary.BigEndian, sum)
		out.Write(body)
		out.Write(checksum.Bytes())
	}
	var header bytes.Buffer
	_ = binary.Write(&header, binary.BigEndian, uint32(width))
	_ = binary.Write(&header, binary.BigEndian, uint32(height))
	header.Write([]byte{8, 2, 0, 0, 0}) // 8-bit, truecolour
	chunk("IHDR", header.Bytes())
	chunk("IDAT", compress(raw.Bytes()))
	chunk("IEND", nil)
	return base64.StdEncoding.EncodeToString(out.Bytes())
}

// compress deflates the PNG's pixel data.
func compress(data []byte) []byte {
	var out bytes.Buffer
	writer, err := zlib.NewWriterLevel(&out, zlib.BestSpeed)
	if err != nil {
		return data
	}
	_, _ = writer.Write(data)
	_ = writer.Close()
	return out.Bytes()
}
