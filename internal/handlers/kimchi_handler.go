package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/providers"
)

// kimchiTopLevelDrops are Anthropic-specific fields stripped before forwarding.
var kimchiTopLevelDrops = []string{
	"anthropic_version",
	"anthropic_beta",
	"client_metadata",
	"mcp_servers",
	"stop_sequences",
	"thinking",
	"top_k",
}

// forwardKimchiRequest forwards requests to Kimchi (OpenAI-format provider).
// Cleans Anthropic-specific fields from the body before forwarding.
func (h *ChatHandler) forwardKimchiRequest(
	w http.ResponseWriter,
	cfg *providers.ProviderConfig,
	apiKey string,
	body []byte,
	isStream bool,
	translateResponse bool,
	metrics *streamMetrics,
) error {
	// 1. Parse body for cleaning
	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return fmt.Errorf("parse request body: %w", err)
	}

	// 2. Clean Anthropic-specific fields
	cleanKimchiBody(reqBody)

	// 3. Re-encode
	cleanedBody, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal cleaned body: %w", err)
	}

	// 4. Create upstream request
	req, err := http.NewRequest(http.MethodPost, cfg.BaseURL, strings.NewReader(string(cleanedBody)))
	if err != nil {
		return fmt.Errorf("create upstream request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	if isStream {
		req.Header.Set("Accept", "text/event-stream")
	}

	resp, err := h.Client.Do(req)
	if err != nil {
		return fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return &upstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	start := time.Now()
	if metrics == nil {
		metrics = &streamMetrics{}
	}
	if isStream {
		return h.handleStreamResponse(w, resp.Body, translateResponse, start, metrics)
	}
	return h.handleJSONResponse(w, resp.Body, translateResponse)
}

// cleanKimchiBody strips Anthropic-specific fields from an OpenAI request body.
func cleanKimchiBody(body map[string]any) {
	if body == nil {
		return
	}

	// Merge Anthropic-style "system" into messages before deleting
	mergeKimchiSystem(body)

	// Drop top-level Anthropic fields
	for _, key := range kimchiTopLevelDrops {
		delete(body, key)
	}
	delete(body, "system")

	// Strip artifacts from messages
	stripKimchiMessageArtifacts(body)

	// Strip artifacts from tools
	stripKimchiToolArtifacts(body)

	// Strip reasoning_content from assistant messages
	stripKimchiReasoningContent(body)
}

// mergeKimchiSystem merges top-level "system" into messages as first system message.
func mergeKimchiSystem(body map[string]any) {
	system, hasSystem := body["system"]
	if !hasSystem {
		return
	}

	systemText := kimchiSystemToText(system)
	if systemText == "" {
		return
	}

	msgs, ok := body["messages"].([]any)
	if !ok {
		return
	}

	// Check for existing system message
	for _, msg := range msgs {
		if m, ok := msg.(map[string]any); ok {
			if role, _ := m["role"].(string); role == "system" {
				// Prepend to existing system content
				switch c := m["content"].(type) {
				case string:
					m["content"] = systemText + "\n\n" + c
				case []any:
					m["content"] = append([]any{map[string]any{"type": "text", "text": systemText}}, c...)
				}
				return
			}
		}
	}

	// No existing system message — prepend one
	body["messages"] = append([]any{map[string]any{"role": "system", "content": systemText}}, msgs...)
}

// kimchiSystemToText converts system field to plain text.
func kimchiSystemToText(system any) string {
	switch v := system.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		var parts []string
		for _, part := range v {
			switch p := part.(type) {
			case string:
				parts = append(parts, p)
			case map[string]any:
				if t, ok := p["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}
	return ""
}

// stripKimchiMessageArtifacts removes Anthropic fields from messages and content blocks.
func stripKimchiMessageArtifacts(body map[string]any) {
	msgs, ok := body["messages"].([]any)
	if !ok {
		return
	}

	for _, msg := range msgs {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		delete(m, "cache_control")

		content, ok := m["content"].([]any)
		if !ok {
			continue
		}

		for i, part := range content {
			p, ok := part.(map[string]any)
			if !ok {
				continue
			}
			delete(p, "cache_control")
			delete(p, "signature")
			content[i] = p
		}
	}
}

// stripKimchiToolArtifacts removes cache_control from tool definitions.
func stripKimchiToolArtifacts(body map[string]any) {
	tools, ok := body["tools"].([]any)
	if !ok {
		return
	}

	for i, tool := range tools {
		t, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		delete(t, "cache_control")
		tools[i] = t
	}
}

// stripKimchiReasoningContent removes long reasoning_content echoed by clients.
func stripKimchiReasoningContent(body map[string]any) {
	msgs, ok := body["messages"].([]any)
	if !ok {
		return
	}

	const placeholderMaxLen = 8
	for _, msg := range msgs {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role == "assistant" {
			if rc, ok := m["reasoning_content"].(string); ok && len(rc) > placeholderMaxLen {
				delete(m, "reasoning_content")
			}
		}
	}
}
