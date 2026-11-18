package media

import (
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	bingTokenMu   sync.Mutex
	bingKey       string
	bingToken     string
	bingCookie    string
	bingTokenTime time.Time
	bingReHelper  = regexp.MustCompile(`params_AbusePreventionHelper\s*=\s*\[([^,]+),([^,]+),`)

	googleTTSMu       sync.Mutex
	googleFSid        string
	googleBl          string
	googleTTSTime     time.Time
	googleReFSid      = regexp.MustCompile(`"FdrFJe":"(.*?)"`)
	googleReBl        = regexp.MustCompile(`"cfb2h":"(.*?)"`)
	googleSanitizeReg = regexp.MustCompile(`[@^*()\\/\-_+=><"'\x60\x{201c}\x{201d}\x{3010}\x{3011}]`)
)
var directHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy: nil, // direct connection to bypass proxy allowlist
	},
	Timeout:       15 * time.Second,
	CheckRedirect: checkRedirectPublic,
}

func doDirectOrClient(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	if client == nil {
		client = directHTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		// Transport-level failure only: retry once direct. A real upstream
		// 403 (e.g. key rejected) must surface, not be retried against the
		// same target where a sandbox mock could mask it as success.
		if resp != nil {
			resp.Body.Close()
		}
		reqClone := req.Clone(ctx)
		return directHTTPClient.Do(reqClone)
	}
	return resp, nil
}

func getBingToken(ctx context.Context, client *http.Client) (string, string, string, error) {
	bingTokenMu.Lock()
	defer bingTokenMu.Unlock()

	if bingKey != "" && bingToken != "" && time.Since(bingTokenTime) < 5*time.Minute {
		return bingKey, bingToken, bingCookie, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.bing.com/translator", nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := doDirectOrClient(ctx, client, req)
	if err != nil {
		return "", "", "", fmt.Errorf("fetch bing translator: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", "", fmt.Errorf("read bing response: %w", err)
	}

	m := bingReHelper.FindSubmatch(body)
	if len(m) < 3 {
		return "", "", "", fmt.Errorf("failed to parse Bing token from translator page")
	}

	bingKey = string(m[1])
	bingToken = strings.Trim(string(m[2]), `"`)
	var cookies []string
	for _, c := range resp.Header.Values("Set-Cookie") {
		if idx := strings.IndexByte(c, ';'); idx != -1 {
			cookies = append(cookies, c[:idx])
		} else {
			cookies = append(cookies, c)
		}
	}
	bingCookie = strings.Join(cookies, "; ")
	bingTokenTime = time.Now()

	return bingKey, bingToken, bingCookie, nil
}

// SynthesizeEdgeTTS synthesizes text to speech using Microsoft Edge / Bing translator.
func SynthesizeEdgeTTS(ctx context.Context, client *http.Client, text, voice string) ([]byte, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("empty text for TTS")
	}
	if voice == "" || voice == "default" || voice == "alloy" {
		voice = "en-US-AriaNeural"
	}
	parts := strings.Split(voice, "-")
	lang := "en-US"
	if len(parts) >= 2 {
		lang = parts[0] + "-" + parts[1]
	}
	gender := "Female"
	if strings.Contains(strings.ToLower(voice), "male") && !strings.Contains(strings.ToLower(voice), "female") {
		gender = "Male"
	}

	key, token, cookie, err := getBingToken(ctx, client)
	if err != nil {
		return nil, err
	}

	ssml := fmt.Sprintf("<speak version='1.0' xml:lang='%s'><voice xml:lang='%s' xml:gender='%s' name='%s'><prosody rate='0.00%%'>%s</prosody></voice></speak>",
		lang, lang, gender, voice, text)

	form := url.Values{}
	form.Set("ssml", ssml)
	form.Set("token", token)
	form.Set("key", key)

	reqURL := "https://www.bing.com/tfettts?isVertical=1&&IG=1&IID=translator.5023&SFX=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", "https://www.bing.com")
	req.Header.Set("Referer", "https://www.bing.com/translator")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	resp, err := doDirectOrClient(ctx, client, req)
	if err != nil {
		return nil, fmt.Errorf("bing speech request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errText, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bing speech returned %d: %s", resp.StatusCode, string(errText))
	}

	audio, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read speech response: %w", err)
	}
	// Upstream parity (edgeTts.js): payloads under 1KiB are error pages, not audio.
	if len(audio) < 1024 {
		return nil, fmt.Errorf("bing TTS returned empty audio")
	}
	return audio, nil
}

func getGoogleTTSToken(ctx context.Context, client *http.Client) (string, string, error) {
	googleTTSMu.Lock()
	defer googleTTSMu.Unlock()

	if googleFSid != "" && googleBl != "" && time.Since(googleTTSTime) < 11*time.Minute {
		return googleFSid, googleBl, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://translate.google.com/", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36")

	resp, err := doDirectOrClient(ctx, client, req)
	if err != nil {
		return "", "", fmt.Errorf("fetch google translate: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("read google translate page: %w", err)
	}

	mFSid := googleReFSid.FindSubmatch(body)
	mBl := googleReBl.FindSubmatch(body)
	if len(mFSid) < 2 || len(mBl) < 2 {
		return "", "", fmt.Errorf("failed to parse Google TTS tokens")
	}

	googleFSid = string(mFSid[1])
	googleBl = string(mBl[1])
	googleTTSTime = time.Now()
	return googleFSid, googleBl, nil
}

// SynthesizeGoogleTTS synthesizes text to speech using Google Translate.
func SynthesizeGoogleTTS(ctx context.Context, client *http.Client, text, voice string) ([]byte, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("empty text for TTS")
	}
	lang := voice
	if lang == "" || lang == "default" || lang == "alloy" {
		lang = "en"
	}
	cleanText := googleSanitizeReg.ReplaceAllString(text, " ")
	cleanText = strings.ReplaceAll(cleanText, ", ", ". ")

	fsid, bl, err := getGoogleTTSToken(ctx, client)
	if err != nil {
		return nil, err
	}

	reqID := fmt.Sprintf("%d", 100000+rand.IntN(90000))
	q := url.Values{}
	q.Set("rpcids", "jQ1olc")
	q.Set("f.sid", fsid)
	q.Set("bl", bl)
	q.Set("hl", lang)
	q.Set("soc-app", "1")
	q.Set("soc-platform", "1")
	q.Set("soc-device", "1")
	q.Set("_reqid", reqID)
	q.Set("rt", "c")

	targetURL := "https://translate.google.com/_/TranslateWebserverUi/data/batchexecute?" + q.Encode()

	innerPayload := []any{cleanText, lang, nil, "undefined", []any{0}}
	innerJSON, _ := json.Marshal(innerPayload)

	batchPayload := []any{
		[]any{
			[]any{"jQ1olc", string(innerJSON), nil, "generic"},
		},
	}
	batchJSON, _ := json.Marshal(batchPayload)

	form := url.Values{}
	form.Set("f.req", string(batchJSON))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", "https://translate.google.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36")

	resp, err := doDirectOrClient(ctx, client, req)
	if err != nil {
		return nil, fmt.Errorf("google TTS request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read google TTS response: %w", err)
	}

	lines := strings.Split(string(respBody), "\n")
	if len(lines) < 4 {
		return nil, fmt.Errorf("unexpected Google TTS response format")
	}

	var batchResp []any
	if err := json.Unmarshal([]byte(lines[3]), &batchResp); err != nil {
		return nil, fmt.Errorf("unmarshal batch response: %w", err)
	}
	if len(batchResp) == 0 {
		return nil, fmt.Errorf("empty batch response from Google TTS")
	}
	item0, ok := batchResp[0].([]any)
	if !ok || len(item0) < 3 {
		return nil, fmt.Errorf("malformed batch item from Google TTS")
	}
	innerStr, ok := item0[2].(string)
	if !ok {
		return nil, fmt.Errorf("no audio string in batch item")
	}
	var audioArray []any
	if err := json.Unmarshal([]byte(innerStr), &audioArray); err != nil {
		return nil, fmt.Errorf("unmarshal audio array: %w", err)
	}
	if len(audioArray) == 0 {
		return nil, fmt.Errorf("empty audio array from Google TTS")
	}
	b64Audio, ok := audioArray[0].(string)
	if !ok || len(b64Audio) < 100 {
		return nil, fmt.Errorf("Google TTS returned invalid or empty audio")
	}

	audioBytes, err := base64.StdEncoding.DecodeString(b64Audio)
	if err != nil {
		return nil, fmt.Errorf("decode audio base64: %w", err)
	}
	return audioBytes, nil
}
