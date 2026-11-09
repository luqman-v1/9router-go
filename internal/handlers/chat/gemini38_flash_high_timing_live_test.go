package chat

import (
	"bufio"
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/proxy/executor"
)

// Timing-focused live e2e for antigravity gemini-3.8-flash-high. The existing
// TestLiveE2E_Gemini38_FlashHigh_RealStream proves the stream works; this one
// measures what a client actually feels over a real HTTP server: when the
// first byte lands, how the events spread out (a streamed answer or one big
// dump at the end), and how long the thinking phase holds the stream.
//
// Same gate as the other live tests: 9ROUTER_LIVE_TESTS=1, make test-live.
func TestLiveE2E_Gemini38_FlashHigh_Timing(t *testing.T) {
	repo, cleanup := getRealUserDB(t)
	defer cleanup()

	conns, err := repo.GetProviderConnections("antigravity", true)
	if err != nil || len(conns) == 0 {
		t.Skip("no active antigravity connections")
	}

	executor.RegisterAll()
	handler := NewChatHandler(repo)

	srv := httptest.NewServer(http.HandlerFunc(handler.HandleChatCompletions))
	defer srv.Close()

	reqBody := `{
		"model": "ag/gemini-3.8-flash-high",
		"messages": [
			{"role": "user", "content": "Count from 1 to 25 separated by commas, then on a new line write DONE."}
		],
		"max_tokens": 2000,
		"stream": true
	}`

	start := time.Now()
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader([]byte(reqBody)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	t.Logf("HTTP status after headers: %d (headers at %s)", resp.StatusCode, time.Since(start).Round(time.Millisecond))

	if resp.StatusCode != http.StatusOK {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		requireLiveOK(t, resp.StatusCode, buf.String())
		return
	}

	var (
		firstByte   time.Duration
		events      int
		arrivals    []string
		firstReason time.Duration
		lastReason  time.Duration
		firstText   time.Duration
		lastEvent   time.Duration
		textChars   int
		reasonChars int
	)
	reader := bufio.NewReader(resp.Body)
	for {
		line, rerr := reader.ReadString('\n')
		now := time.Since(start)
		if firstByte == 0 && len(line) > 0 {
			firstByte = now
		}
		payload := strings.TrimSpace(line)
		if strings.HasPrefix(payload, "data: ") {
			data := strings.TrimPrefix(payload, "data: ")
			if data != "[DONE]" {
				events++
				lastEvent = now
				if len(arrivals) < 10 {
					arrivals = append(arrivals, now.Round(time.Millisecond).String())
				}
				var ev struct {
					Choices []struct {
						Delta struct {
							Content          *string `json:"content"`
							ReasoningContent *string `json:"reasoning_content"`
						} `json:"delta"`
					} `json:"choices"`
				}
				if json.Unmarshal([]byte(data), &ev) == nil && len(ev.Choices) > 0 {
					if ev.Choices[0].Delta.ReasoningContent != nil {
						reasonChars += len(*ev.Choices[0].Delta.ReasoningContent)
						if firstReason == 0 {
							firstReason = now
						}
						lastReason = now
					}
					if ev.Choices[0].Delta.Content != nil {
						textChars += len(*ev.Choices[0].Delta.Content)
						if firstText == 0 {
							firstText = now
						}
					}
				}
			}
		}
		if rerr != nil {
			break
		}
	}
	total := time.Since(start)

	t.Logf("TTFB (first byte):        %s", firstByte.Round(time.Millisecond))
	t.Logf("first reasoning token:    %s", firstReason.Round(time.Millisecond))
	t.Logf("last reasoning token:     %s", lastReason.Round(time.Millisecond))
	t.Logf("first content token:      %s", firstText.Round(time.Millisecond))
	t.Logf("last event:               %s", lastEvent.Round(time.Millisecond))
	t.Logf("total:                    %s", total.Round(time.Millisecond))
	t.Logf("events:                   %d (reasoning %d chars, content %d chars)", events, reasonChars, textChars)
	t.Logf("first %d arrival gaps:    %v", len(arrivals), arrivals)

	if events == 0 {
		t.Fatal("no SSE events arrived")
	}
	if firstText == 0 {
		t.Errorf("stream never carried content (reasoning %d chars only)", reasonChars)
	}
}
