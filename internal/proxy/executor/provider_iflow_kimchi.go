package executor

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/proxy"
)

func ForwardIflow(w http.ResponseWriter, req *Request) error {
	var reqMap map[string]any
	if err := json.Unmarshal(req.Body, &reqMap); err != nil {
		return fmt.Errorf("parse body: %w", err)
	}
	if req.IsStream {
		reqMap["stream"] = true
		if _, ok := reqMap["stream_options"]; !ok {
			reqMap["stream_options"] = map[string]any{"include_usage": true}
		}
	}
	reqBody, err := marshalStable(reqMap)
	if err != nil {
		return fmt.Errorf("marshal iflow body: %w", err)
	}

	// HMAC-SHA256 signature
	sessionID := "session-" + uuid.New().String()
	timestamp := time.Now().UnixMilli()
	userAgent := "iFlow-Cli"
	payload := userAgent + ":" + sessionID + ":" + strconv.FormatInt(timestamp, 10)

	mac := hmac.New(sha256.New, []byte(req.APIKey))
	mac.Write([]byte(payload))
	signature := hex.EncodeToString(mac.Sum(nil))

	extraHeaders := map[string]string{
		"User-Agent":        userAgent,
		"session-id":        sessionID,
		"x-iflow-timestamp": strconv.FormatInt(timestamp, 10),
		"x-iflow-signature": signature,
	}
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.ForwardIflow(ctx, req.Client, req.Config, req.APIKey, reqBody, req.IsStream, extraHeaders)
	if err != nil {
		return fmt.Errorf("ForwardIflow upstream: %w", err)
	}
	defer resp.Body.Close()

	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	return jsonResponse(req.Ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}

// ForwardKimchi forwards to kimchi with Anthropic field stripping.
// Cleans Anthropic-specific fields from body before forwarding.
func ForwardKimchi(w http.ResponseWriter, req *Request) error {
	var reqBody map[string]any
	if err := json.Unmarshal(req.Body, &reqBody); err != nil {
		return fmt.Errorf("parse request body: %w", err)
	}

	CleanKimchiBody(reqBody)

	cleanedBody, err := marshalStable(reqBody)
	if err != nil {
		return fmt.Errorf("marshal cleaned body: %w", err)
	}

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.ForwardKimchi(ctx, req.Client, req.Config, req.APIKey, cleanedBody, req.IsStream)
	if err != nil {
		return fmt.Errorf("ForwardKimchi: %w", err)
	}
	defer resp.Body.Close()

	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	return jsonResponse(req.Ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}

// ForwardKiro forwards to kiro with AWS EventStream response handling.
// Uses EventStream binary parsing instead of standard SSE.
//
// The kiro.dev gateway only accepts a conversationState envelope and answers
// 400 "Improperly formed request." for anything else, so the OpenAI body is
// translated first (internal/translator.OpenAIToKiro). Bodies that already
// carry a conversationState (MITM passthrough) are forwarded untouched.
