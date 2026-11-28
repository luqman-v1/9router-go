package executor

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strings"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

func ForwardCodex(w http.ResponseWriter, req *Request) error {
	transformedBody, _, err := buildResponsesBody(req.Body)
	if err != nil {
		return fmt.Errorf("transform body: %w", err)
	}
	responsesLite := false
	if req.Provider != "perplexity-agent" {
		transformedBody, responsesLite, err = shapeCodexResponsesBody(transformedBody, codexLiteRequested(req.Ctx))
		if err != nil {
			return fmt.Errorf("shape Codex body: %w", err)
		}
	}

	// Resolve conversation-stable session_id for Codex prompt caching
	sessionID := req.SessionID
	if sessionID == "" {
		if req.ConnectionID != "" {
			sessionID = req.ConnectionID
		} else if req.ConnData != nil {
			if cid, ok := req.ConnData["connectionId"].(string); ok && cid != "" {
				sessionID = cid
			}
		}
	}
	if sessionID == "" {
		sessionID = "default"
	}

	// Inject prompt_cache_key for stable Codex prompt caching if not already set
	transformedBody = injectCodexPromptCacheKey(transformedBody, sessionID)

	// The ChatGPT Codex backend rejects unsupported parameters and requires the allowlist
	transformedBody = stripCodexUnsupportedTokenParams(transformedBody, responsesLite)
	// Only now that the "(level)" suffix is gone can the catalog id be mapped
	// to its wire id, so this has to follow the transform rather than precede it.
	transformedBody = rewriteCodexUpstreamModel(transformedBody)
	cfg, transformedBody := applyCodexCompact(req.Config, transformedBody)
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.ForwardCodex(ctx, req.Client, cfg, req.APIKey, transformedBody, req.IsStream, req.ConnData, responsesLite, sessionID)
	if err != nil {
		return fmt.Errorf("ForwardCodex: %w", err)
	}
	defer resp.Body.Close()
	return handleCodexStream(w, req, resp.Body)
}

func injectCodexPromptCacheKey(body []byte, sessionID string) []byte {
	if sessionID == "" || sessionID == "default" {
		return body
	}
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return body
	}
	if pck, ok := envelope["prompt_cache_key"].(string); ok && pck != "" {
		return body
	}
	envelope["prompt_cache_key"] = sessionID
	out, err := marshalStable(envelope)
	if err != nil {
		return body
	}
	return out
}

// applyCodexCompact routes a compaction request to the codex /compact endpoint
// and strips the marker from the body, porting open-sse/executors/codex.js
// transformRequest + buildUrl. Without it the flag would ride upstream as an
// unknown field and the request would take the normal /responses path, so the
// client would get an answer where it asked for a compacted transcript.
func applyCodexCompact(cfg *providers.ProviderConfig, body []byte) (*providers.ProviderConfig, []byte) {
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return cfg, body
	}
	compact, ok := envelope["_compact"].(bool)
	if !ok || !compact {
		return cfg, body
	}
	delete(envelope, "_compact")
	out, err := marshalStable(envelope)
	if err != nil {
		return cfg, body
	}
	cloned := *cfg
	cloned.BaseURL = strings.TrimRight(cloned.BaseURL, "/") + "/compact"
	return &cloned, out
}

// ForwardIflow forwards to iflow with HMAC-SHA256 signature.
// Injects stream_options and generates HMAC headers before forwarding.
