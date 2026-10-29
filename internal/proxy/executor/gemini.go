package executor

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// geminiStream replays a live Gemini-native SSE body as OpenAI chunks.
// Served from here by ForwardOpencodeGo's gemini route; the antigravity path
// streams through the chat handler instead (forwardGeminiNativeRequest),
// which unwraps the {"response": ...} envelope per frame.
func geminiStream(w http.ResponseWriter, upstream io.Reader, ctx context.Context) error {
	hw := proxy.NewHeartbeatWriter(ctx, w, 0)
	defer hw.Close()
	flusher := proxy.WriteSSEHeaders(hw)
	firstLine := true
	state := &translator.GeminiStreamState{SessionID: handlerutil.GetSessionID(ctx)}
	return proxy.ScanStream(upstream, func(chunk []byte) {
		if firstLine {
			firstLine = false
			return
		}
		cs := strings.TrimSpace(string(chunk))
		if cs == "" || cs == "data: [DONE]" {
			return
		}
		if !strings.HasPrefix(cs, "data: ") {
			return
		}
		oc, err := translator.TranslateGeminiChunkToOpenAI([]byte(strings.TrimPrefix(cs, "data: ")), state)
		if err != nil || oc == nil {
			return
		}
		hw.Write(oc)
		hw.Write([]byte("\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	})
}

func geminiNonStream(w http.ResponseWriter, upstream io.Reader) error {
	body, err := io.ReadAll(io.LimitReader(upstream, constants.MaxUpstreamBodyBytes))
	if err != nil {
		return err
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
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return nil
	}
	resp := translator.OpenAIResponse{
		ID:    "chatcmpl-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		Model: "gemini",
	}
	if geminiResp.UsageMetadata != nil {
		resp.Usage = &translator.OpenAIUsage{
			PromptTokens:     geminiResp.UsageMetadata.PromptTokenCount,
			CompletionTokens: geminiResp.UsageMetadata.CandidatesTokenCount,
		}
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
			Index:        0,
			Message:      translator.OpenAIRespMsg{Role: "assistant", Content: text},
			FinishReason: &fr,
		})
	}
	openaiResp, err := json.Marshal(resp)
	if err != nil {
		// Fall back to writing the raw Gemini response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(openaiResp)
	return nil
}
