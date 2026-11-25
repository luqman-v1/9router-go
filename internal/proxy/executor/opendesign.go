package executor

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	json "encoding/json/v2"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// Open Design OpenDesign Cloud (open-design.ai) / AMR.
//
// Two planes on two hosts: model traffic rides the runtime key (`sk-amr-…`) at
// https://amr-link.open-design.ai/v1 on the lane each model declares (chat
// completions default; Responses for claude-haiku-5.5 / deepseek-v4-pro;
// Anthropic's messages for glm-5.3-flash), while plan/wallet/allowance read
// from https://amr-api.open-design.ai on the control key (`vela_ctrl_…`).
//
// A connection's APIKey holds whichever credential was pasted; a pasted
// `vela_ctrl_…` value is a control key and rides no model request. The control
// key rides in ProviderSpecificData["controlKey"] (see the dashboard's
// AddConnectionModal), and requests routed here already split it from the
// runtime key. If APIKey is empty or control-prefixed, the request has no
// runtime credential and is refused before a wasted dial.
//
// Every model is served on all three lanes; the declared one below is only the
// default. signFor follows the request path: /messages takes x-api-key +
// anthropic-version, everything else takes Authorization: Bearer.
const (
	openDesignChatPath      = "/chat/completions"
	openDesignResponsesPath = "/responses"
	openDesignMessagesPath  = "/messages"
)

// ForwardOpenDesign routes one request onto the lane its model declares.
func ForwardOpenDesign(w http.ResponseWriter, req *Request) error {
	cleanModel := openDesignModel(req.Body)
	if cleanModel == "" {
		return errors.New("opendesign: missing model")
	}
	switch providers.GetOpenDesignLane(cleanModel) {
	case providers.FormatOpenAIResponses:
		return forwardOpenDesignLane(w, req, cleanModel, openDesignResponsesPath, false)
	case providers.FormatClaude:
		return forwardOpenDesignLane(w, req, cleanModel, openDesignMessagesPath, true)
	default:
		return forwardOpenDesignLane(w, req, cleanModel, openDesignChatPath, false)
	}
}

// openDesignModel reads the model the caller asked for and strips the provider
// prefix plus any thinking suffix, so upstream receives the bare id.
func openDesignModel(body []byte) string {
	var reqObj struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqObj); err != nil {
		return ""
	}
	return stripOpenCodePrefix(reqObj.Model, "opendesign", "od")
}

// forwardOpenDesignLane builds the header set for the lane, sets the lane URL,
// and forwards. The lane carries the runtime key: the control key never rides a
// model request. Non-200 statuses become *proxy.UpstreamError so 401/403/429
// are preserved instead of being written as a 200 carrying an error body.
func forwardOpenDesignLane(w http.ResponseWriter, req *Request, cleanModel, lanePath string, isMessages bool) error {
	if req.APIKey == "" || strings.HasPrefix(req.APIKey, "vela_ctrl_") {
		return errors.New("opendesign: no runtime key (sk-amr-…) configured for this connection; the Design Plan key cannot serve model requests")
	}

	cfg := *req.Config
	base := strings.TrimRight(cfg.BaseURL, "/")
	for _, lane := range []string{openDesignChatPath, openDesignResponsesPath, openDesignMessagesPath} {
		if strings.HasSuffix(base, lane) {
			base = strings.TrimSuffix(base, lane)
			break
		}
	}
	cfg.BaseURL = base + lanePath

	headers := map[string]string{
		"Content-Type": "application/json",
	}
	if isMessages {
		// Anthropic Messages takes x-api-key + the version pin (the plugin's
		// signFor reads the API off the request path).
		headers["x-api-key"] = req.APIKey
		headers["anthropic-version"] = "2023-06-01"
	} else {
		headers["Authorization"] = "Bearer " + req.APIKey
	}
	if req.Config != nil {
		for k, v := range req.Config.StaticHeaders {
			headers[k] = v
		}
		for k, v := range req.Config.HeaderOverrides {
			headers[k] = v
		}
	}

	ctx := reqCtx(req)
	client := req.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := proxy.DoRequest(ctx, client, "POST", cfg.BaseURL, headers, req.Body)
	if err != nil {
		return fmt.Errorf("opendesign forward: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	for k, v := range resp.Header {
		for _, vv := range v {
			w.Header().Add(k, vv)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("opendesign write response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if rerr != nil {
			return fmt.Errorf("opendesign read error body: %w", rerr)
		}
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: body, Header: resp.Header}
	}
	return nil
}
