package proxy

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"encoding/json/jsontext"
	json "encoding/json/v2"

	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/aws"
)

// bedrockConnectTimeout bounds dialling a Bedrock runtime host. It is not a whole-request
// deadline: DoRequest replays the signed request on transient upstream failures, and a
// stalled dial must not eat the client's whole timeout budget.
const bedrockConnectTimeout = 30 * time.Second

// Bedrock in-band error types, surfaced verbatim so a throttled or validation-failed
// call is not reported to the client as a generic failure.
const (
	bedrockErrValidation   = "ValidationException"
	bedrockErrThrottling   = "ThrottlingException"
	bedrockErrService      = "ServiceUnavailableException"
	bedrockErrAccessDenied = "AccessDeniedException"
)

// ForwardBedrock calls the Amazon Bedrock runtime.
//
// Auth is SigV4, signed per request from credentials resolved by internal/proxy/aws. That
// is what gives this provider real AWS SSO support: a connection can name a local AWS
// profile instead of carrying keys, and every request re-resolves, so an
// `aws sso login` session is picked up and refreshed without touching the connection.
//
// One executor serves both Bedrock entries, the way Vertex serves vertex and
// vertex-partner: the registry entry's Format decides the wire shape. "claude" for the
// Anthropic models, which take the Anthropic Messages body; empty for bedrock-xai, where
// Grok speaks OpenAI Chat Completions.
func ForwardBedrock(ctx context.Context, client *http.Client, cfg *providers.ProviderConfig,
	providerID, apiKey string, body []byte, isStream bool, psd map[string]any) (*http.Response, error) {

	// A per-connection base URL wins over the registry entry: a custom node aimed at a
	// VPC endpoint must not be rewritten to the public regional host.
	if base := psdStr(psd, "baseUrl"); base != "" {
		cloned := *cfg
		cloned.BaseURL = base
		cfg = &cloned
	}

	resolver := aws.NewResolver()
	resolved, err := resolver.Resolve(ctx, BedrockCredentialInput(apiKey, psd))
	if err != nil {
		return nil, err
	}

	model := BedrockModelID(body)
	if ok, hint := providers.BedrockModelFamily(providerID, model); !ok {
		// Fail here rather than mid-stream: a model from the wrong family returns chunks
		// this executor cannot read, and discovering that after the upstream call has been
		// billed is a worse experience than an upfront message naming the limitation.
		// Encoded rather than interpolated into a literal: the model id reaches the
		// message verbatim, and a quote inside it would otherwise produce a body the
		// client's own JSON parser rejects in place of reading the reason.
		body, err := json.Marshal(map[string]any{
			"error": map[string]any{
				"message": fmt.Sprintf(
					"Bedrock model %q is not supported by the %s provider, which expects %s. "+
						"Model families on Bedrock use different request and response shapes, so each "+
						"gets its own provider entry rather than failing mid-stream after the call is billed.",
					model, providerID, hint),
				"type": "invalid_request_error",
				"code": "invalid_model",
			},
		})
		if err != nil {
			return nil, fmt.Errorf("encode bedrock model-family error: %w", err)
		}
		return nil, &UpstreamError{StatusCode: http.StatusBadRequest, Body: body}
	}

	endpoint := bedrockRuntimeURL(cfg, resolved.Region, model, isStream)

	payload, err := BedrockRequestBody(body, cfg)
	if err != nil {
		return nil, err
	}

	accept := "application/json"
	if isStream {
		accept = "application/vnd.amazon.eventstream"
	}
	// Content-Length is deliberately not signed: the transport sets it itself, and signing
	// a value the runtime may normalise differently is a needless SignatureDoesNotMatch.
	headers, err := aws.SignRequest(aws.SignOptions{
		Method:  http.MethodPost,
		URL:     endpoint,
		Headers: map[string]string{"Content-Type": "application/json", "Accept": accept},
		Body:    string(payload),
		Region:  resolved.Region,
		Service: providers.BedrockService,
		Credentials: aws.Credentials{
			AccessKeyID:     resolved.Credentials.AccessKeyID,
			SecretAccessKey: resolved.Credentials.SecretAccessKey,
			SessionToken:    resolved.Credentials.SessionToken,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("sign bedrock request: %w", err)
	}

	connectCtx, cancel := context.WithTimeout(ctx, bedrockConnectTimeout)
	defer cancel()

	resp, err := DoRequest(connectCtx, client, http.MethodPost, endpoint, headers, payload)
	if err != nil {
		return nil, fmt.Errorf("forward to %s: %w", endpoint, err)
	}
	return resp, nil
}

// bedrockRuntimeURL builds the signed request URL.
//
// The host is regionalised from the validated region — an unvalidated region would let a
// connection redirect signed traffic to an arbitrary origin — unless the connection or a
// custom node names its own base URL, which is how a VPC endpoint or a local fake is
// dialed instead of the public runtime.
//
// The model id is escaped once here; the signer escapes it a second time for the
// canonical request, which is what Bedrock expects for a ":0"-suffixed version.
func bedrockRuntimeURL(cfg *providers.ProviderConfig, region, model string, isStream bool) string {
	action := providers.BedrockInvokePath
	if isStream {
		action = providers.BedrockStreamPath
	}
	base := fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", region)
	if cfg != nil {
		if override := strings.TrimSpace(cfg.BaseURL); override != "" && !strings.Contains(override, "{region}") {
			base = strings.TrimSuffix(override, "/")
		}
	}
	return fmt.Sprintf("%s/model/%s/%s", base, url.PathEscape(model), action)
}

// BedrockCredentialInput maps a connection onto the resolver's input shape.
//
// The secret access key travels as the connection's API key, matching the credential
// form the dashboard renders; the key id, session token and profile are provider-specific
// data alongside it.
func BedrockCredentialInput(apiKey string, psd map[string]any) aws.ProfileInput {
	in := aws.ProfileInput{SecretAccessKey: apiKey}
	if psd == nil {
		return in
	}
	in.AccessKeyID = psdStr(psd, "accessKeyId")
	in.SessionToken = psdStr(psd, "sessionToken")
	in.Profile = psdStr(psd, "profile")
	in.Region = psdStr(psd, "region")
	return in
}

// BedrockModelID reads the routed model from the request body, dropping the provider
// prefix the client sent ("br/us.anthropic.…"). The body is authoritative: the chat
// handler rewrites the routed id on the way in.
func BedrockModelID(body []byte) string {
	var parsed struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	model := strings.TrimSpace(parsed.Model)
	if _, after, ok := strings.Cut(model, "/"); ok {
		return after
	}
	return model
}

// BedrockRequestBody adapts the client's body to what Bedrock accepts.
//
// Bedrock takes the Anthropic body but rejects `model` (it lives in the URL) and
// `stream` (streaming is chosen by the endpoint), and requires `anthropic_version`
// instead. Only the Anthropic wire wants that pin, and it goes after the spread because
// a claude-format client may carry its own version, and letting that win earns a
// ValidationException.
func BedrockRequestBody(body []byte, cfg *providers.ProviderConfig) ([]byte, error) {
	var parsed map[string]jsontext.Value
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("bedrock: parse request body: %w", err)
	}
	delete(parsed, "model")
	delete(parsed, "stream")
	if cfg == nil || cfg.Format == "claude" {
		quoted, err := json.Marshal(providers.BedrockAnthropicVersion)
		if err != nil {
			return nil, fmt.Errorf("bedrock: encode anthropic_version: %w", err)
		}
		parsed["anthropic_version"] = quoted
	}
	out, err := json.Marshal(parsed)
	if err != nil {
		return nil, fmt.Errorf("bedrock: rebuild request body: %w", err)
	}
	return out, nil
}

// BedrockStreamEvent is one decoded EventStream chunk: its headers plus the inner event
// as raw JSON, so the caller can re-marshal it in whatever wire shape its client wants.
type BedrockStreamEvent struct {
	Headers map[string]string
	Payload map[string]jsontext.Value
}

// DecodeBedrockEvents drains an EventStream body into decoded chunks.
//
// The streaming path in the executor decodes incrementally rather than buffering a whole
// response; this whole-body entry point exists for tests and for the paths that must see
// the complete stream before answering.
func DecodeBedrockEvents(upstream io.Reader) ([]BedrockStreamEvent, error) {
	reader := providers.NewEventStreamReader(upstream)
	var events []BedrockStreamEvent
	for {
		frame, err := reader.ReadFrame()
		if err != nil {
			return events, err
		}
		if frame == nil {
			return events, nil
		}
		// Bedrock reports throttling and validation failures as in-band frames, not HTTP
		// status codes, so they must surface instead of looking like a clean end of stream.
		if err := bedrockFrameFailure(frame); err != nil {
			return events, err
		}
		if frame.Headers[":event-type"] != providers.BedrockChunkEvent {
			// InvokeModelWithResponseStream defines no other event type today, so an
			// unknown one means AWS extended the protocol. Failing would break every stream
			// over what may be a harmless metadata event; skipping silently could hide
			// lost content, so it is logged.
			log.Warn("proxy", "bedrock skipped unrecognised EventStream event type",
				"eventType", frame.Headers[":event-type"])
			continue
		}
		inner, err := bedrockChunkPayload(frame.Payload)
		if err != nil {
			return events, err
		}
		var decoded map[string]jsontext.Value
		if err := json.Unmarshal(inner, &decoded); err != nil {
			return events, fmt.Errorf("bedrock: chunk is not a JSON object: %w", err)
		}
		events = append(events, BedrockStreamEvent{Headers: frame.Headers, Payload: decoded})
	}
}

// bedrockChunkPayload base64-decodes a chunk frame's `bytes` field into the Anthropic (or
// Chat Completions) event it carries.
func bedrockChunkPayload(payload []byte) ([]byte, error) {
	var wrapper struct {
		Bytes string `json:"bytes"`
	}
	if err := json.Unmarshal(payload, &wrapper); err != nil || wrapper.Bytes == "" {
		// A CRC-valid chunk with no payload means the protocol changed under us. Dropping
		// it would silently lose content, so it is a failure.
		return nil, fmt.Errorf("bedrock: chunk frame carried no payload bytes")
	}
	decoded, err := base64.StdEncoding.DecodeString(wrapper.Bytes)
	if err != nil {
		return nil, fmt.Errorf("bedrock: chunk bytes are not valid base64: %w", err)
	}
	return decoded, nil
}

// bedrockFrameFailure turns an in-band Bedrock exception frame into an error carrying the
// upstream's own error type, so a throttled call is not reported as a generic failure.
func bedrockFrameFailure(frame *providers.EventStreamFrame) error {
	messageType := frame.Headers[":message-type"]
	if messageType != "exception" && messageType != "error" {
		return nil
	}
	exceptionType := frame.Headers[":exception-type"]
	if exceptionType == "" {
		exceptionType = frame.Headers[":error-code"]
	}
	if exceptionType == "" {
		exceptionType = bedrockErrValidation
	}

	message := frame.Headers[":error-message"]
	if message == "" {
		var payload struct {
			Message string `json:"message"`
			Capital string `json:"Message"`
		}
		if _, decodeErr := providers.DecodeEventPayload(frame.Payload, &payload); decodeErr == nil {
			if payload.Message != "" {
				message = payload.Message
			} else if payload.Capital != "" {
				message = payload.Capital
			}
		}
	}
	if message == "" {
		message = "bedrock returned an EventStream " + messageType
	}

	body, err := json.Marshal(map[string]any{
		"error": map[string]any{"message": message, "type": exceptionType, "code": "bedrock_error"},
	})
	if err != nil {
		return fmt.Errorf("encode bedrock exception error: %w", err)
	}
	return &UpstreamError{StatusCode: bedrockUpstreamStatus(exceptionType), Body: body}
}

func bedrockUpstreamStatus(exceptionType string) int {
	switch exceptionType {
	case bedrockErrThrottling:
		return http.StatusTooManyRequests
	case bedrockErrService:
		return http.StatusServiceUnavailable
	case bedrockErrAccessDenied:
		return http.StatusForbidden
	default:
		return http.StatusBadRequest
	}
}

// BedrockProbe validates a Bedrock connection with a signed ListFoundationModels call,
// for the dashboard's validate and Test paths.
//
// Resolution failures — incomplete keys, an expired SSO session, a bad region — come
// back as the message, since they are exactly what the user needs to fix.
//
// AccessDeniedException counts as VALID: the signature was accepted, and whether this
// particular identity may list models says nothing about whether it may invoke them.
func BedrockProbe(ctx context.Context, client *http.Client, apiKey string, psd map[string]any) (bool, string) {
	resolved, err := aws.NewResolver().Resolve(ctx, BedrockCredentialInput(apiKey, psd))
	if err != nil {
		return false, err.Error()
	}

	// Resolve validated the region, so it is safe in the hostname.
	endpoint := fmt.Sprintf("https://bedrock.%s.amazonaws.com/%s", resolved.Region, providers.BedrockProbePath)
	headers, err := aws.SignRequest(aws.SignOptions{
		Method:  http.MethodGet,
		URL:     endpoint,
		Headers: map[string]string{"Accept": "application/json"},
		Region:  resolved.Region,
		Service: providers.BedrockService,
		Credentials: aws.Credentials{
			AccessKeyID:     resolved.Credentials.AccessKeyID,
			SecretAccessKey: resolved.Credentials.SecretAccessKey,
			SessionToken:    resolved.Credentials.SessionToken,
		},
	})
	if err != nil {
		return false, fmt.Sprintf("sign bedrock credential probe: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, fmt.Sprintf("build bedrock credential probe: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Sprintf("bedrock credential probe failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return true, ""
	}

	// AWS names the failure in this header, e.g.
	// "AccessDeniedException:bedrock:ListFoundationModels".
	errorType := strings.SplitN(resp.Header.Get(providers.BedrockErrorTypeHeader), ":", 2)[0]
	if errorType == providers.BedrockAccessDeniedError {
		return true, ""
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var payload struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &payload)
	reason := errorType
	if reason == "" {
		reason = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	if payload.Message != "" {
		reason += ": " + payload.Message
	}
	return false, "AWS rejected the credentials (" + reason + ")"
}

// psdStr reads a trimmed string from a provider-specific-data map.
func psdStr(psd map[string]any, key string) string {
	if psd == nil {
		return ""
	}
	value, _ := psd[key].(string)
	return strings.TrimSpace(value)
}
