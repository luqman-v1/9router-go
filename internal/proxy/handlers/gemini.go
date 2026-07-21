package proxyhandlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// ForwardGemini forwards an OpenAI-format request to a Gemini-native endpoint.
// projectID is non-empty for antigravity (cloudcode-pa.googleapis.com).
func ForwardGemini(w http.ResponseWriter, client *http.Client, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream, translateResponse bool, projectID, modelName string) error {
	resp, err := proxy.ForwardGemini(client, cfg, apiKey, string(body), isStream, projectID, modelName)
	if err != nil {
		return fmt.Errorf("forward to Gemini: %w", err)
	}
	defer resp.Body.Close()

	// Unwrap antigravity envelope
	if projectID != "" {
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			resp.Body.Close()
			return fmt.Errorf("read antigravity response body: %w", err)
		}
		resp.Body.Close()
		unwrapped := translator.UnwrapAntigravityResponse(raw)
		if isStream {
			return geminiStream(w, io.NopCloser(bytes.NewReader(unwrapped)), translateResponse)
		}
		return geminiNonStream(w, bytes.NewReader(unwrapped))
	}
	if isStream {
		return geminiStream(w, resp.Body, translateResponse)
	}
	return geminiNonStream(w, resp.Body)
}

func geminiStream(w http.ResponseWriter, upstream io.Reader, translateResponse bool) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	firstLine := true
	state := &translator.GeminiStreamState{}

	return proxy.ScanStream(upstream, func(chunk []byte) {
		if firstLine {
			firstLine = false
			return
		}
		chunkStr := strings.TrimSpace(string(chunk))
		if chunkStr == "" || chunkStr == "data: [DONE]" {
			return
		}
		if !strings.HasPrefix(chunkStr, "data: ") {
			return
		}
		dataStr := strings.TrimPrefix(chunkStr, "data: ")
		openaiChunk, err := translator.TranslateGeminiChunkToOpenAI([]byte(dataStr), state)
		if err != nil || openaiChunk == nil {
			if err != nil {
				log.Printf("[gemini_stream] translate error: %v", err)
			}
			return
		}
		if _, werr := w.Write(openaiChunk); werr != nil {
			log.Printf("[gemini_stream] write chunk: %v", werr)
		}
		if _, werr := w.Write([]byte("\n\n")); werr != nil {
			log.Printf("[gemini_stream] write separator: %v", werr)
		}
		if flusher != nil {
			flusher.Flush()
		}
	})
}

func geminiNonStream(w http.ResponseWriter, upstream io.Reader) error {
	body, err := io.ReadAll(upstream)
	if err != nil {
		return fmt.Errorf("read Gemini non-stream body: %w", err)
	}

	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
				Role string `json:"role"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata *struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return nil
	}

	usage := &translator.OpenAIUsage{}
	if geminiResp.UsageMetadata != nil {
		usage.PromptTokens = geminiResp.UsageMetadata.PromptTokenCount
		usage.CompletionTokens = geminiResp.UsageMetadata.CandidatesTokenCount
	}
	resp := translator.OpenAIResponse{
		ID:      "chatcmpl-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		Model:   "gemini",
		Usage:   usage,
	}
	for _, c := range geminiResp.Candidates {
		text := ""
		if len(c.Content.Parts) > 0 {
			text = c.Content.Parts[0].Text
		}
		fr := "stop"
		if c.FinishReason != "" && c.FinishReason != "STOP" {
			fr = strings.ToLower(c.FinishReason)
		}
		resp.Choices = append(resp.Choices, translator.OpenAIResponseChoice{
			Index: 0,
			Message: translator.OpenAIRespMsg{
				Role:    "assistant",
				Content: text,
			},
			FinishReason: &fr,
		})
	}
	openaiResp, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal OpenAI response: %w", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, werr := w.Write(openaiResp); werr != nil {
		return fmt.Errorf("write Gemini non-stream response: %w", werr)
	}
	return nil
}
