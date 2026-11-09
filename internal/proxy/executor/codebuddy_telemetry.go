package executor

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// CodeBuddy CN anti-ban telemetry.
//
// Port of upstream `open-sse/services/codebuddyTelemetry.js` (PR decolua/9router
// #3257). Replicates the official CodeBuddy client's telemetry so a proxied key
// no longer appears "silent" to Tencent's ban detector.
//
// DESIGN RULES:
//   - Every function swallows its own errors and never returns an error to the
//     caller.
//   - All sends are fire-and-forget from the chat path: the caller launches a
//     goroutine and the telemetry never adds latency to, or changes the
//     outcome of, a chat request.
//   - Raw secrets are never sent. Only the derived identity and the bearer the
//     key already owns are transmitted.
//
// Endpoints (official client):
//   - POST https://copilot.tencent.com/v2/report        (lifecycle + chat events)
//   - POST https://galileotelemetry.tencent.com/collect (analytics heartbeat)
//   - POST https://galileotelemetry.tencent.com/v1/traces (otel spans)

var codebuddyReportURL = "https://copilot.tencent.com/v2/report"

const (
	codebuddyGalileoCollectURL = "https://galileotelemetry.tencent.com/collect"
	codebuddyGalileoTracesURL  = "https://galileotelemetry.tencent.com/v1/traces"
	codebuddyGalileoAegisURL   = "https://galileotelemetry.tencent.com/aegiscontrol/whitelist"
	codebuddyGalileoTopic      = "SDK-768de26ec97715a3bbab"
	codebuddyTelemetryTimeout  = 5 * time.Second
	codebuddyWorkBuddyVersion  = "5.3.8"
)

func codebuddyAccessToken(c codebuddyCredential) string {
	if c.AccessToken != "" {
		return c.AccessToken
	}
	return c.APIKey
}

func codebuddyUID(c codebuddyCredential) string {
	if c.UID != "" {
		return c.UID
	}
	return ""
}

// codebuddyTelemetryAuthHeaders mirrors upstream authHeaders().
func codebuddyTelemetryAuthHeaders(c codebuddyCredential) map[string]string {
	token := codebuddyAccessToken(c)
	uid := codebuddyUID(c)
	h := map[string]string{
		"Content-Type":        "application/json;charset=UTF-8",
		"Accept":              "application/json",
		"User-Agent":          "CLI/" + codebuddyCLIVersion + " CodeBuddy/" + codebuddyCLIVersion,
		"X-Product":           "SaaS",
		"X-Domain":            "www.codebuddy.cn",
		"X-IDE-Type":          "CLI",
		"X-IDE-Name":          "CLI",
		"X-IDE-Version":       codebuddyCLIVersion,
		"x-requested-with":    "XMLHttpRequest",
		"x-codebuddy-request": "1",
	}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	if uid != "" {
		h["X-User-Id"] = uid
	}
	return h
}

// codebuddyEvent merges per-event fields with common event metadata.
func codebuddyEvent(c codebuddyCredential, fields map[string]any) map[string]any {
	id := getCodebuddyIdentity(c)
	hw := id.Hardware
	vcs := id.VCS

	out := make(map[string]any, len(fields)+25)
	out["userId"] = codebuddyUID(c)
	out["product"] = "SaaS"
	out["releaseDate"] = id.Build.ReleaseDate
	out["commit"] = id.Build.Commit
	out["extName"] = "workbuddy-desktop"
	out["extVersion"] = codebuddyWorkBuddyVersion
	out["ideName"] = "CLI"
	out["ideType"] = "CLI"
	out["ideVersion"] = id.CLIVersion
	out["machineId"] = id.MachineID
	out["sessionId"] = id.SessionID
	out["qimei36"] = id.Qimei36
	out["os"] = hw.OS
	out["arch"] = hw.Arch
	out["osVersion"] = hw.OSVersion
	out["cpuModel"] = hw.CPUModel
	out["cpuCores"] = hw.CPUCores
	out["memorySize"] = hw.MemorySize
	out["timezone"] = hw.Timezone
	out["vcsType"] = "git"
	out["vcsRepo"] = vcs.VCSRepo
	out["vcsBranchName"] = vcs.VCSBranch
	out["vcsRevId"] = vcs.VCSRevID

	for k, v := range fields {
		out[k] = v
	}
	return out
}

func codebuddyTelemetryJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func codebuddyTelemetryClient() *http.Client {
	return &http.Client{Timeout: codebuddyTelemetryTimeout}
}

func codebuddyTelemetryPost(ctx context.Context, client *http.Client, targetURL string, headers map[string]string, body []byte) {
	if body == nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err == nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

func codebuddyTelemetryGet(ctx context.Context, client *http.Client, targetURL string, headers map[string]string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err == nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

func codebuddyPostReport(ctx context.Context, client *http.Client, events []map[string]any, c codebuddyCredential) {
	if len(events) == 0 {
		return
	}
	if codebuddyAccessToken(c) == "" {
		return
	}
	body := codebuddyTelemetryJSON(events)
	if body == nil {
		return
	}
	codebuddyTelemetryPost(ctx, client, codebuddyReportURL, codebuddyTelemetryAuthHeaders(c), body)
}

// lifecycleSent guards the once-per-credential lifecycle events.
var (
	codebuddyLifecycleMu   sync.Mutex
	codebuddyLifecycleSent = make(map[string]bool)
)

// codebuddySendSessionLifecycle sends the session-startup lifecycle events once
// per credential per process. Non-blocking and fail-open.
func codebuddySendSessionLifecycle(parent context.Context, c codebuddyCredential) {
	codebuddyLifecycleMu.Lock()
	dedupKey := deriveMachineID(c)
	if codebuddyLifecycleSent[dedupKey] {
		codebuddyLifecycleMu.Unlock()
		return
	}
	codebuddyLifecycleSent[dedupKey] = true
	codebuddyLifecycleMu.Unlock()

	if codebuddyAccessToken(c) == "" {
		return
	}

	ctx, cancel := context.WithTimeout(parent, codebuddyTelemetryTimeout)
	defer cancel()
	client := codebuddyTelemetryClient()

	id := getCodebuddyIdentity(c)
	now := time.Now().UnixMilli()

	lifecycleText, _ := json.Marshal(map[string]any{
		"sessionId":  id.SessionID,
		"platform":   id.Hardware.OS,
		"arch":       id.Hardware.Arch,
		"appVersion": codebuddyCLIVersion,
		"startedAt":  now,
	})

	events := []map[string]any{
		codebuddyEvent(c, map[string]any{
			"eventCode":   "ide_lifecycle",
			"timestamp":   now,
			"reportDelay": 2000,
			"action":      "start",
			"text":        string(lifecycleText),
		}),
		codebuddyEvent(c, map[string]any{
			"eventCode":   "plugin_status",
			"timestamp":   now,
			"reportDelay": 3000,
			"status":      "info",
			"text":        "pluginStart",
			"isInterval":  false,
		}),
	}
	codebuddyPostReport(ctx, client, events, c)

	// Aegis security whitelist check (startup signal).
	if uid := codebuddyUID(c); uid != "" {
		targetURL := fmt.Sprintf("%s?uid=%s&topic=%s", codebuddyGalileoAegisURL, url.QueryEscape(uid), codebuddyGalileoTopic)
		codebuddyTelemetryGet(ctx, client, targetURL, codebuddyTelemetryAuthHeaders(c))
	}
}

// codebuddyPreChatContext carries per-request correlation identifiers.
type codebuddyPreChatContext struct {
	RequestID      string
	ConversationID string
	MessageID      string
	Model          string
	ModelID        string
	Mode           string
	InputLength    int
	HistoryCount   int
}

func (p codebuddyPreChatContext) conversationOrRequest() string {
	if p.ConversationID != "" {
		return p.ConversationID
	}
	return p.RequestID
}

// codebuddySendPreChatEvents sends agent_task_created + chat_message_send +
// chat_request_send before the chat call. Fail-open and non-blocking.
func codebuddySendPreChatEvents(parent context.Context, c codebuddyCredential, pctx codebuddyPreChatContext) {
	if codebuddyAccessToken(c) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(parent, codebuddyTelemetryTimeout)
	defer cancel()
	client := codebuddyTelemetryClient()

	now := time.Now().UnixMilli()
	model := pctx.Model
	if model == "" {
		model = "auto"
	}
	modelID := pctx.ModelID
	if modelID == "" {
		modelID = model
	}
	mode := pctx.Mode
	if mode == "" {
		mode = "craft"
	}
	histCount := pctx.HistoryCount
	if histCount <= 0 {
		histCount = 1
	}

	events := []map[string]any{
		codebuddyEvent(c, map[string]any{
			"eventCode":        "agent_task_created",
			"timestamp":        now - 2000,
			"reportDelay":      1000,
			"name":             "working",
			"mode":             mode,
			"source":           "LOCAL",
			"has_repo":         false,
			"repo_type":        "none",
			"workspace_type":   "empty",
			"has_connector":    false,
			"has_expert":       false,
			"has_skill":        false,
			"requestModelName": model,
			"requestModelId":   modelID,
			"conversationId":   pctx.conversationOrRequest(),
			"requestId":        pctx.RequestID,
		}),
		codebuddyEvent(c, map[string]any{
			"eventCode":          "chat_message_send",
			"timestamp":          now - 1500,
			"reportDelay":        1000,
			"conversationId":     pctx.conversationOrRequest(),
			"requestId":          pctx.RequestID,
			"messageId":          pctx.MessageID,
			"requestModelId":     modelID,
			"requestModelName":   model,
			"historyCount":       histCount,
			"isContextTruncated": false,
			"currentStepCount":   1,
			"presentAt":          now - 1500,
		}),
		codebuddyEvent(c, map[string]any{
			"eventCode":        "chat_request_send",
			"timestamp":        now - 1000,
			"reportDelay":      1000,
			"mode":             mode,
			"inputLength":      pctx.InputLength,
			"requestModelId":   modelID,
			"requestModelName": model,
		}),
	}
	codebuddyPostReport(ctx, client, events, c)
}

// codebuddyPostChatContext carries the outcome of a chat request.
type codebuddyPostChatContext struct {
	Model            string
	ModelID          string
	RequestID        string
	ConversationID   string
	InputToken       int
	OutputToken      int
	IsSuccessful     bool
	MessageErrorCode string
	FinishReason     string
	DurationMs       int64
}

// codebuddySendPostChatEvents sends chat_message_response + agent_task_completed.
func codebuddySendPostChatEvents(parent context.Context, c codebuddyCredential, post codebuddyPostChatContext) {
	if codebuddyAccessToken(c) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(parent, codebuddyTelemetryTimeout)
	defer cancel()
	client := codebuddyTelemetryClient()

	now := time.Now().UnixMilli()
	model := post.Model
	if model == "" {
		model = "auto"
	}
	modelID := post.ModelID
	if modelID == "" {
		modelID = model
	}
	finishReason := post.FinishReason
	if finishReason == "" {
		finishReason = "stop"
	}

	events := []map[string]any{
		codebuddyEvent(c, map[string]any{
			"eventCode":        "chat_message_response",
			"timestamp":        now - 500,
			"reportDelay":      1000,
			"requestModelId":   modelID,
			"requestModelName": model,
			"responseModelId":  modelID,
			"inputToken":       post.InputToken,
			"outputToken":      post.OutputToken,
			"totalToken":       post.InputToken + post.OutputToken,
			"cachedTokens":     0,
			"isSuccessful":     post.IsSuccessful,
			"messageErrorCode": post.MessageErrorCode,
			"finishReason":     finishReason,
			"firstTokenAt":     now - 400,
			"presentAt":        now - 500,
		}),
		codebuddyEvent(c, map[string]any{
			"eventCode":   "agent_task_completed",
			"timestamp":   now,
			"reportDelay": 1000,
			"mode":        "LOCAL",
			"task_id":     post.RequestID,
			"duration_ms": post.DurationMs,
			"total_steps": 1,
		}),
	}
	codebuddyPostReport(ctx, client, events, c)
}

// codebuddySendGalileoTrace sends an OpenTelemetry trace span for the chat turn.
func codebuddySendGalileoTrace(parent context.Context, c codebuddyCredential, durationMs int64) {
	if codebuddyAccessToken(c) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(parent, codebuddyTelemetryTimeout)
	defer cancel()
	client := codebuddyTelemetryClient()

	id := getCodebuddyIdentity(c)
	now := time.Now().UnixMilli()
	durNs := durationMs * 1_000_000
	endNs := now * 1_000_000
	startNs := endNs - durNs
	if startNs < 0 {
		startNs = 0
	}

	traceSeed := fmt.Sprintf("%s%d", id.Qimei36, now)
	cleanSeed := lowerHex(traceSeed + id.Qimei36)
	if len(cleanSeed) < 32 {
		cleanSeed = (cleanSeed + "00000000000000000000000000000000")[:32]
	} else {
		cleanSeed = cleanSeed[:32]
	}
	traceID := cleanSeed
	spanID := cleanSeed[:16]

	bodyMap := map[string]any{
		"resourceSpans": []map[string]any{
			{
				"resource": map[string]any{
					"attributes": []map[string]any{
						{"key": "service.name", "value": map[string]any{"stringValue": "codebuddy-cli"}},
						{"key": "telemetry.sdk.language", "value": map[string]any{"stringValue": "nodejs"}},
						{"key": "telemetry.sdk.name", "value": map[string]any{"stringValue": "opentelemetry"}},
						{"key": "service.version", "value": map[string]any{"stringValue": codebuddyCLIVersion}},
						{"key": "env_name", "value": map[string]any{"stringValue": "production"}},
					},
				},
				"scopeSpans": []map[string]any{
					{
						"scope": map[string]any{"name": "codebuddy-cli"},
						"spans": []map[string]any{
							{
								"traceId":           traceID,
								"spanId":            spanID,
								"name":              "wb.chat.request",
								"kind":              1,
								"startTimeUnixNano": fmt.Sprintf("%d", startNs),
								"endTimeUnixNano":   fmt.Sprintf("%d", endNs),
							},
						},
					},
				},
			},
		},
	}
	body := codebuddyTelemetryJSON(bodyMap)
	headers := map[string]string{
		"Content-Type": "application/json",
		"User-Agent":   "CLI/" + codebuddyCLIVersion + " CodeBuddy/" + codebuddyCLIVersion,
	}
	codebuddyTelemetryPost(ctx, client, codebuddyGalileoTracesURL, headers, body)
}
