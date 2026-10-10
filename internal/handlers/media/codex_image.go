package media

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/usagetracker"
)

// Codex has no image endpoint of its own: its image models are Responses-API
// models that emit an image_generation_call item. Before this lane existed a
// /v1/images/generations request for one of them fell through the generic
// forwarder and was POSTed to <base>/v1/images/generations, which the ChatGPT
// backend does not serve — and, because nothing parsed the upstream frames,
// every Codex image turn was recorded with zero tokens. Upstream parity:
// open-sse/handlers/imageProviders/codex.js, and commit e10da160 for the usage
// accounting this lane threads through the response path.

const (
	codexImageModelSuffix = "-image"
	codexImageRefDetail   = "high"
	// The gpt-image-* ids name the tool's own model, so the Responses call is
	// answered by the main model and the id travels in the tool definition.
	codexImagesMainModel = "gpt-5.5"
)

var codexToolImageModels = map[string]bool{
	"gpt-image-1.5":          true,
	"gpt-image-2":            true,
	"gpt-image-2.5":          true,
	"gpt-image-2.5-flare":    true,
	"gpt-image-2.5-sunburst": true,
}

// codexImageRequest is the OpenAI image shape a caller posts, plus the
// reference images the Responses content array carries.
type codexImageRequest struct {
	Prompt       string   `json:"prompt"`
	Model        string   `json:"model"`
	Image        string   `json:"image"`
	Images       []string `json:"images"`
	ImageDetail  string   `json:"image_detail"`
	Size         string   `json:"size"`
	Quality      string   `json:"quality"`
	Background   string   `json:"background"`
	OutputFormat string   `json:"output_format"`
	// N is parsed only so the caller's copy of the field is not silently
	// dropped: upstream answers with exactly one image regardless, and
	// returning two would mean a second Responses round trip.
	N int `json:"n"`
}

// codexImageUsage is the recorded shape. Cached and reasoning tokens are
// carried only when the upstream sent usable integers, mirroring
// normalizeResponsesUsage.
type codexImageUsage struct {
	PromptTokens     int  `json:"prompt_tokens"`
	CompletionTokens int  `json:"completion_tokens"`
	TotalTokens      int  `json:"total_tokens"`
	CachedTokens     *int `json:"cached_tokens,omitempty"`
	ReasoningTokens  *int `json:"reasoning_tokens,omitempty"`
}

// handleCodexImage serves a Codex image generation request through the
// Responses API, rotating accounts in the same shape as the Antigravity image
// lane.
func (h *MediaHandler) handleCodexImage(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	var req codexImageRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return fmt.Errorf("parse codex image request: %w", err)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return fmt.Errorf("missing required field: prompt")
	}

	// The router already resolved the model id for this request, so that is the
	// authoritative spelling; the raw body field only fills in for a direct
	// call, where it may still carry the provider prefix a client typed.
	model := modelInfo.Model
	if model == "" {
		model = strings.TrimSpace(req.Model)
		model = strings.TrimPrefix(model, modelInfo.Provider+"/")
		model = strings.TrimPrefix(model, "cx/")
	}
	cleanModel, responsesModel, toolModel := codexImageModels(model)

	pinned := modelInfo.ConnectionID
	if pinned == "" {
		pinned = imagePinnedConnectionID(r)
	}
	usePinned := pinned != ""
	var excludeIDs []string
	var lastErr error
	for {
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), "codex", pinned, excludeIDs, cleanModel)
		if err != nil || conn == nil {
			if lastErr != nil {
				return lastErr
			}
			return fmt.Errorf("no active connection for codex: %w", err)
		}
		attemptErr := h.tryCodexImageConn(w, r, &req, cleanModel, responsesModel, toolModel, conn, connData)
		if attemptErr == nil {
			return nil
		}
		lastErr = attemptErr
		if usePinned {
			return lastErr
		}
		excludeIDs = append(excludeIDs, conn.ID)
	}
}

// codexImageModels splits the catalog id into the model that answers the
// Responses call and the image-generation tool model it carries, mirroring
// resolveCodexImageModels upstream.
func codexImageModels(model string) (cleanModel, responsesModel, toolModel string) {
	model = strings.TrimSpace(model)
	if codexToolImageModels[model] {
		return model, codexImagesMainModel, model
	}
	stripped := strings.TrimSuffix(model, codexImageModelSuffix)
	return stripped, stripped, ""
}

// codexImageRefs normalizes the caller's reference images into data URLs or
// remote URLs the Responses content array accepts.
func codexImageRefs(req *codexImageRequest) []string {
	refs := make([]string, 0, len(req.Images)+1)
	for _, img := range req.Images {
		if url := codexImageDataURL(img); url != "" {
			refs = append(refs, url)
		}
	}
	if single := codexImageDataURL(req.Image); single != "" {
		refs = append(refs, single)
	}
	return refs
}

// codexImageDataURL passes an already-addressable input through untouched and
// wraps a bare base64 payload, which is what the dashboard and most clients
// post.
func codexImageDataURL(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	if strings.HasPrefix(input, "data:image/") {
		return input
	}
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		return input
	}
	return "data:image/png;base64," + input
}

// codexImageContent builds the Responses message content: each reference is
// bracketed by a text marker so the model can tell the images apart, then the
// prompt.
func codexImageContent(prompt string, refs []string, detail string) []any {
	content := make([]any, 0, len(refs)*3+1)
	for i, ref := range refs {
		content = append(content,
			map[string]any{"type": "input_text", "text": fmt.Sprintf("<image name=image%d>", i+1)},
			map[string]any{"type": "input_image", "image_url": ref, "detail": detail},
			map[string]any{"type": "input_text", "text": "</image>"},
		)
	}
	return append(content, map[string]any{"type": "input_text", "text": prompt})
}

// codexImageBody assembles the Responses request upstream sends.
func codexImageBody(req *codexImageRequest, responsesModel, toolModel string) ([]byte, error) {
	detail := req.ImageDetail
	if detail == "" {
		detail = codexImageRefDetail
	}
	outputFormat := strings.ToLower(req.OutputFormat)
	if outputFormat == "" {
		outputFormat = "png"
	}

	imageTool := map[string]any{
		"type":          "image_generation",
		"output_format": outputFormat,
	}
	refs := codexImageRefs(req)
	toolChoice := any("auto")
	if toolModel != "" {
		action := "generate"
		if len(refs) > 0 {
			action = "edit"
		}
		imageTool["action"] = action
		imageTool["model"] = toolModel
		toolChoice = map[string]any{"type": "image_generation"}
	}
	for key, value := range map[string]string{"size": req.Size, "quality": req.Quality, "background": req.Background} {
		if value != "" {
			imageTool[key] = value
		}
	}

	payload := map[string]any{
		"model":               responsesModel,
		"instructions":        "",
		"input":               []any{map[string]any{"type": "message", "role": "user", "content": codexImageContent(req.Prompt, refs, detail)}},
		"tools":               []any{imageTool},
		"tool_choice":         toolChoice,
		"parallel_tool_calls": false,
		"prompt_cache_key":    uuid.New().String(),
		"stream":              true,
		"store":               false,
	}
	if toolModel != "" {
		// The tool path is the only one that has to be told to think, and the
		// Responses lane rejects a null reasoning object.
		payload["reasoning"] = map[string]any{"effort": "medium", "summary": "auto"}
	}

	out, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal codex image body: %w", err)
	}
	return out, nil
}

// tryCodexImageConn performs one Codex image attempt and writes the response.
func (h *MediaHandler) tryCodexImageConn(w http.ResponseWriter, r *http.Request, req *codexImageRequest, cleanModel, responsesModel, toolModel string, conn *models.ProviderConnection, connData *chat.ConnectionData) error {
	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		return fmt.Errorf("no API key found for codex connection %s", conn.ID)
	}
	refreshed, _, err := h.ChatH.RefreshOAuthTokenIfExpired(conn.ID, apiKey)
	if err == nil && refreshed != "" {
		apiKey = refreshed
	}

	providerCfg, err := h.ChatH.GetProviderConfig("codex", connData)
	if err != nil {
		return fmt.Errorf("get codex config: %w", err)
	}
	targetURL := strings.TrimRight(providerCfg.BaseURL, "/")
	if connData != nil && strings.TrimSpace(connData.BaseURL) != "" {
		targetURL = strings.TrimRight(strings.TrimSpace(connData.BaseURL), "/")
	}
	if targetURL == "" {
		return fmt.Errorf("no Codex responses endpoint configured")
	}

	payload, err := codexImageBody(req, responsesModel, toolModel)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create codex image request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream, application/json")
	for k, v := range providerCfg.StaticHeaders {
		httpReq.Header.Set(k, v)
	}
	handlerutil.SetAuthHeader(httpReq, apiKey, providerCfg.AuthHeader, providerCfg.AuthScheme)

	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		return clientErr
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("codex image request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return h.codexImageUpstreamError(r, resp.StatusCode, respBody, conn, cleanModel)
	}

	image, usage := parseCodexImageStream(resp.Body)
	if image == "" {
		return fmt.Errorf("Codex did not return an image. Account may not be entitled (Plus/Pro required).")
	}

	h.recordCodexImageUsage(usage, cleanModel, conn, connData)
	h.finishCodexImageConn(conn, cleanModel)
	return writeCodexImageJSON(w, image)
}

// codexImageUpstreamError classifies a failed upstream response and locks the
// account so the rotation moves on instead of hammering it. A probe reads
// production state to decide what to display and must never clear or extend it.
func (h *MediaHandler) codexImageUpstreamError(r *http.Request, status int, body []byte, conn *models.ProviderConnection, cleanModel string) error {
	text := string(body)
	if len(text) > 300 {
		text = text[:300]
	}
	log.Warn("media", "codex image error", "status", status, "body", text)
	if h.Repo != nil && conn != nil && !handlerutil.IsProbeContext(r.Context()) {
		if classification := providers.ClassifyError(status, text, 0); classification.ShouldFallback {
			cooldownSec := max(classification.CooldownMs/1000, 1)
			_ = h.Repo.LockConnectionModel(conn.ID, cleanModel, cooldownSec, classification.NewBackoffLevel)
		}
	}
	return fmt.Errorf("codex image failed with status %d: %s", status, text)
}

// finishCodexImageConn marks the account used and records the recent request
// the dashboard shows.
func (h *MediaHandler) finishCodexImageConn(conn *models.ProviderConnection, cleanModel string) {
	if h.Repo != nil && conn != nil {
		h.Repo.UpdateConnectionLastUsed(conn.ID)
	}
	usagetracker.GetTracker().PushRecent(usagetracker.RecentRequest{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Model:     cleanModel,
		Provider:  "codex",
		Status:    "ok",
	}, h.Repo)
}

// recordCodexImageUsage writes the token row the upstream reported. An image
// turn with no usage is recorded as nothing at all rather than as zeros: a zero
// row is indistinguishable from a turn that was never billed.
func (h *MediaHandler) recordCodexImageUsage(usage *codexImageUsage, cleanModel string, conn *models.ProviderConnection, connData *chat.ConnectionData) {
	if usage == nil || h.Repo == nil {
		return
	}
	connID := ""
	if conn != nil {
		connID = conn.ID
	}
	connKey := ""
	if connData != nil {
		connKey = chat.ExtractAPIKey(connData)
	}
	cached := 0
	if usage.CachedTokens != nil {
		cached = *usage.CachedTokens
	}
	reasoning := 0
	if usage.ReasoningTokens != nil {
		reasoning = *usage.ReasoningTokens
	}
	meta := fmt.Sprintf(`{"provider":"codex","model":"%s","connectionId":"%s"}`, cleanModel, connID)
	tokens := fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,"cached_tokens":%d,"reasoning_tokens":%d}`,
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, cached, reasoning)
	if err := h.Repo.InsertUsageHistory("codex", cleanModel, connID, maskMediaAPIKey(connKey), "/v1/images/generations",
		usage.PromptTokens, usage.CompletionTokens, 0, "success", usage.TotalTokens, meta, tokens); err != nil {
		log.Error("media", "codex image usage insert failed", "error", err)
	}
}

// maskMediaAPIKey hides everything but the tail of a key before it reaches the
// usage table.
func maskMediaAPIKey(key string) string {
	if len(key) <= 8 {
		return key
	}
	return strings.Repeat("*", len(key)-4) + key[len(key)-4:]
}

// writeCodexImageJSON answers a collecting caller in the OpenAI image shape.
func writeCodexImageJSON(w http.ResponseWriter, imageB64 string) error {
	payload := map[string]any{
		"created": time.Now().Unix(),
		"data":    []any{map[string]any{"b64_json": imageB64}},
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal codex image response: %w", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
	return nil
}
