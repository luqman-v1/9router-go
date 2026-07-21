package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// ForwardGemini forwards to gemini-native endpoints (antigravity).
func ForwardGemini(w http.ResponseWriter, req *Request) error {
	resp, err := proxy.ForwardGemini(req.Client, req.Config, req.APIKey, string(req.Body), req.IsStream, req.ProjectID, req.ModelName)
	if err != nil {
		return fmt.Errorf("ForwardGemini: %w", err)
	}
	defer resp.Body.Close()

	if req.ProjectID != "" {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		unwrapped := translator.UnwrapAntigravityResponse(raw)
		if req.IsStream {
			return geminiStream(w, io.NopCloser(bytes.NewReader(unwrapped)))
		}
		return geminiNonStream(w, bytes.NewReader(unwrapped))
	}
	if req.IsStream {
		resp.Body = proxy.NewStallReader(resp.Body, 0, "gemini")
		return geminiStream(w, resp.Body)
	}
	return geminiNonStream(w, resp.Body)
}

func geminiStream(w http.ResponseWriter, upstream io.Reader) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	firstLine := true
	state := &translator.GeminiStreamState{}
	return proxy.ScanStream(upstream, func(chunk []byte) {
		if firstLine { firstLine = false; return }
		cs := strings.TrimSpace(string(chunk))
		if cs == "" || cs == "data: [DONE]" { return }
		if !strings.HasPrefix(cs, "data: ") { return }
		oc, err := translator.TranslateGeminiChunkToOpenAI([]byte(strings.TrimPrefix(cs, "data: ")), state)
		if err != nil || oc == nil { return }
		w.Write(oc)
		w.Write([]byte("\n\n"))
		if flusher != nil { flusher.Flush() }
	})
}

func geminiNonStream(w http.ResponseWriter, upstream io.Reader) error {
	body, err := io.ReadAll(upstream)
	if err != nil { return err }
	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct { Text string `json:"text"` } `json:"parts"`
				Role string `json:"role"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata *struct {
			PromptTokenCount int `json:"promptTokenCount"`
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
		ID: "chatcmpl-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		Model: "gemini",
	}
	if geminiResp.UsageMetadata != nil {
		resp.Usage = &translator.OpenAIUsage{
			PromptTokens: geminiResp.UsageMetadata.PromptTokenCount,
			CompletionTokens: geminiResp.UsageMetadata.CandidatesTokenCount,
		}
	}
	for _, c := range geminiResp.Candidates {
		text := ""
		if len(c.Content.Parts) > 0 { text = c.Content.Parts[0].Text }
		fr := "stop"
		if c.FinishReason != "" && c.FinishReason != "STOP" { fr = strings.ToLower(c.FinishReason) }
		resp.Choices = append(resp.Choices, translator.OpenAIResponseChoice{
			Index: 0,
			Message: translator.OpenAIRespMsg{Role: "assistant", Content: text},
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
