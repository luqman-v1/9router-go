package proxy

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http"
	"strconv"
	"strings"

	"9router/proxy/internal/handlerutil"
)

// defaultInbandErrorStatus is reported when a provider-injected error event
// carries no usable status of its own. It matches the rest of the relay,
// where an unusable upstream body is a 502 the fallback layer retries on.
const defaultInbandErrorStatus = http.StatusBadGateway

// InbandStreamErrorMessage is the gateway wording for a turn killed by a
// provider-injected error event. The provider's own message is appended after
// the colon, so the failure stays diagnosable without parroting provider
// phrasing ("JSON error injected into SSE stream") as the whole reason.
const InbandStreamErrorMessage = "upstream event stream carried an error"

// DetectInbandSSEError inspects one raw SSE line for a provider-injected
// error event: a `data:` payload (or a bare JSON line) whose top-level
// `error` field is present and non-null. OpenRouter relays a dead downstream
// this way (`choices: []` plus `error: {code, message}` on HTTP 200), and
// Claude-style `event: error` frames carry the same shape in their data line.
//
// It is provider-agnostic on purpose: any `error` object fails the turn, no
// provider id or model substring is ever consulted. A nil error, `[DONE]`,
// comments, heartbeats and plain content lines all report not-found.
func DetectInbandSSEError(line []byte) (code int, message string, found bool) {
	payload := inbandPayload(line)
	if payload == nil {
		return 0, "", false
	}
	return parseInbandError(payload)
}

// isEventErrorLine reports a standalone `event: error` label line. The label
// carries no payload itself; the following data line holds the error object
// DetectInbandSSEError reads. Swallowing the label alongside its data line
// keeps the client from seeing a dangling event with no data.
func isEventErrorLine(line []byte) bool {
	rest, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("event:"))
	if !ok {
		return false
	}
	return string(bytes.TrimSpace(rest)) == "error"
}

// inbandPayload extracts the JSON document from one SSE line, or nil when
// the line cannot carry an error event. The `"error"` substring check is a
// cheap prefilter only; the JSON parse in parseInbandError is authoritative,
// so content that merely mentions the word error never matches.
func inbandPayload(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] == ':' {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) {
		return nil
	}
	payload := trimmed
	if rest, ok := bytes.CutPrefix(trimmed, []byte("data:")); ok {
		payload = bytes.TrimSpace(rest)
	}
	if bytes.Equal(payload, []byte("[DONE]")) {
		return nil
	}
	if len(payload) == 0 || payload[0] != '{' {
		return nil
	}
	if !bytes.Contains(payload, []byte(`"error"`)) {
		return nil
	}
	return payload
}

// inbandEnvelope reads only the field that decides failure.
type inbandEnvelope struct {
	Error jsontext.Value `json:"error"`
}

// inbandErrorObject reads the failure detail out of an error object. Code
// and Status stay raw: providers disagree on the key and on number-vs-string,
// and httpStatusFromJSON normalizes both.
type inbandErrorObject struct {
	Message string         `json:"message"`
	Type    string         `json:"type"`
	Code    jsontext.Value `json:"code"`
	Status  jsontext.Value `json:"status"`
}

// parseInbandError classifies the payload inbandPayload extracted. Any
// present non-null `error` value fails the turn, even one whose shape is
// unknown — no successful stream ever carries an `error` key.
func parseInbandError(payload []byte) (int, string, bool) {
	var env inbandEnvelope
	if err := json.Unmarshal(payload, &env, handlerutil.UpstreamBody); err != nil {
		return 0, "", false
	}
	raw := bytes.TrimSpace(env.Error)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, "", false
	}
	if raw[0] == '"' {
		var msg string
		if err := json.Unmarshal(raw, &msg, handlerutil.UpstreamBody); err != nil || strings.TrimSpace(msg) == "" {
			return 0, "", false
		}
		return defaultInbandErrorStatus, inbandMessage(msg), true
	}
	if raw[0] != '{' {
		return defaultInbandErrorStatus, inbandMessage(""), true
	}
	var obj inbandErrorObject
	if err := json.Unmarshal(raw, &obj, handlerutil.UpstreamBody); err != nil {
		return defaultInbandErrorStatus, inbandMessage(""), true
	}
	msg := obj.Message
	if msg == "" {
		msg = obj.Type
	}
	return inbandStatusCode(obj.Code, obj.Status), inbandMessage(msg), true
}

// inbandMessage words the gateway error, keeping the provider text for
// diagnosis and classification.
func inbandMessage(providerMsg string) string {
	if strings.TrimSpace(providerMsg) == "" {
		return InbandStreamErrorMessage
	}
	return InbandStreamErrorMessage + ": " + strings.TrimSpace(providerMsg)
}

// inbandStatusCode returns the first usable HTTP status from the candidates,
// defaulting to 502. Non-numeric codes (gateway-style `"upstream_error"`,
// `"overloaded_error"`) and out-of-range numbers never pass through.
func inbandStatusCode(candidates ...jsontext.Value) int {
	for _, raw := range candidates {
		if code := httpStatusFromJSON(bytes.TrimSpace(raw)); code != 0 {
			return code
		}
	}
	return defaultInbandErrorStatus
}

// httpStatusFromJSON reads an HTTP status from a JSON number or numeric
// string. Anything outside 400-599 is not a usable error status here.
func httpStatusFromJSON(raw []byte) int {
	if len(raw) == 0 {
		return 0
	}
	var num float64
	if err := json.Unmarshal(raw, &num, handlerutil.UpstreamBody); err == nil {
		if num >= 400 && num < 600 {
			return int(num)
		}
		return 0
	}
	var str string
	if err := json.Unmarshal(raw, &str, handlerutil.UpstreamBody); err != nil {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(str)); err == nil && n >= 400 && n < 600 {
		return n
	}
	return 0
}
