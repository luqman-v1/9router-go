package providers

import (
	json "encoding/json/v2"
	"strconv"
	"strings"
)

// DeprecationStatus is how a model reached the deprecated state.
type DeprecationStatus string

const (
	// DeprecationUnknown is the zero value: nothing has said anything about
	// the model, so the dashboard must not render a badge.
	DeprecationUnknown DeprecationStatus = ""
	// DeprecationGone was learned from an upstream HTTP 410.
	DeprecationGone DeprecationStatus = "gone"
	// DeprecationRetired was learned from a successful catalogue sync that no
	// longer lists the model.
	DeprecationRetired DeprecationStatus = "retired"
)

// ModelDeprecation is what the gateway knows about one provider's model: when
// it was found dead, and what the upstream said about it.
//
// Key is "<provider>/<model>" — the same wire shape as a combo entry, so a
// caller holding a combo target needs no separate resolution step.
type ModelDeprecation struct {
	Provider   string            `json:"provider"`
	Model      string            `json:"model"`
	Status     DeprecationStatus `json:"status"`
	Message    string            `json:"message,omitempty"`
	Successor  string            `json:"successor,omitempty"`
	DetectedAt string            `json:"detectedAt,omitempty"`
}

// DeprecationKey builds the storage key for a provider/model pair. Both parts
// are lowercased because provider ids and model ids reach this from a combo
// entry, a connection row and a dashboard query, and the three spellings do
// not always agree on case.
func DeprecationKey(provider, model string) string {
	return strings.ToLower(provider) + "/" + strings.ToLower(model)
}

// SplitDeprecationKey is the inverse of DeprecationKey.
func SplitDeprecationKey(key string) (provider, model string, ok bool) {
	slash := strings.Index(key, "/")
	if slash <= 0 || slash == len(key)-1 {
		return "", "", false
	}
	return key[:slash], key[slash+1:], true
}

// DeprecationKeyForEntry builds the key from a "provider/model" combo entry.
// An entry with no slash is a bare model with no provider attached and cannot
// be filed, so ok is false.
func DeprecationKeyForEntry(entry string) (string, bool) {
	slash := strings.Index(entry, "/")
	if slash <= 0 || slash == len(entry)-1 {
		return "", false
	}
	return DeprecationKey(entry[:slash], entry[slash+1:]), true
}

// deprecationErrorPayload is the shape providers spell a dead model with: an
// `error` object whose `type`/`code` names the reason and whose `message` says
// what to use instead. The fields are read leniently because the status is
// the load-bearing signal and the payload is only there to tell the operator
// what happened.
//
// `error` is an object in the OpenAI shape and a bare string in the Google /
// Freebuff shape, so it is decoded as `any` and narrowed by the readers
// below instead of being declared twice under two field names.
type deprecationErrorPayload struct {
	Error   any    `json:"error"`
	Message string `json:"message"`
	Type    string `json:"type"`
}

// deprecationObject is the OpenAI-shaped `error` object.
type deprecationObject struct {
	Type        string `json:"type"`
	Code        any    `json:"code"`
	Message     string `json:"message"`
	Detail      string `json:"detail"`
	Status      string `json:"status"`
	Successor   string `json:"successor"`
	ReplacedBy  string `json:"replaced_by"`
	Replacement string `json:"replacement"`
}

// deprecationMarkers are the payload tokens that name a dead model. The
// status alone is not enough: 410 also means an expired OAuth device code
// (handlers/oauth/device.go) and an expired Freebuff session
// (proxy/executor/freebuff.go), and treating either as a model retirement
// would blacklist a model that is serving fine.
var deprecationMarkers = []string{
	// Structured spellings first: an `error.type` that names the condition
	// outright is stronger evidence than any prose reading below it.
	"modeldeprecated",
	"model_deprecated",
	"model-deprecated",
	"modelnotfound",
	"model_not_found",
	"model-retired",
	"modelretired",
	"modelnotavailable",
	"model_not_available",
	"modelunavailable",
	"model_unavailable",
	// Prose spellings. "deprecated" alone is safe only because the status has
	// already been pinned to 410: a 410 is a deprecation unless the payload
	// says otherwise, so the wording only has to catch the providers that
	// write the reason as a sentence rather than a code.
	"model is deprecated",
	"model has been deprecated",
	"model was deprecated",
	"model is retired",
	"model was retired",
	"model is no longer available",
	"model is not available",
	"model has been retired",
}

// IsModelDeprecation reports whether an upstream failure means this model is
// gone for good, as opposed to an account or transport problem.
//
// The HTTP 410 status is required first: it is the one signal every provider
// that retires a model agrees on, and it keeps a same-worded 400 or 404
// request error from blacklisting a model. The payload is then read for the
// reason and the operator-facing message.
func IsModelDeprecation(statusCode int, body []byte) bool {
	if statusCode != 410 {
		return false
	}
	// A 410 with no parseable payload is still a Gone. The payload check
	// exists to keep unrelated 410s out, not to gate the status.
	return deprecationReason(body) != "" || !hasErrorEnvelope(body)
}

// deprecationReason returns the lowercase token naming why the model is
// gone, or "" when the payload names no known reason.
func deprecationReason(body []byte) string {
	payload, ok := decodeDeprecation(body)
	if !ok {
		return ""
	}
	var tokens []string
	tokens = append(tokens, payload.Type, payload.Message)
	if obj, isObject := deprecationObjectOf(payload.Error); isObject {
		tokens = append(tokens,
			obj.Type,
			deprecationCodeString(obj.Code),
			obj.Status,
			obj.Message,
		)
	}
	haystack := strings.ToLower(strings.Join(tokens, " "))
	for _, marker := range deprecationMarkers {
		if strings.Contains(haystack, marker) {
			return marker
		}
	}
	return ""
}

// hasErrorEnvelope reports whether the body looks like an error document at
// all. A payload that does is only a deprecation when its reason matches; one
// that is not (an expired device code, an expired session) is not.
func hasErrorEnvelope(body []byte) bool {
	payload, ok := decodeDeprecation(body)
	if !ok {
		return false
	}
	if payload.Message != "" || payload.Type != "" {
		return true
	}
	switch errValue := payload.Error.(type) {
	case nil:
		return false
	case string:
		return errValue != ""
	default:
		return true
	}
}

// DeprecationDetail is the operator-facing reading of a deprecation payload.
type DeprecationDetail struct {
	Message   string
	Successor string
}

// ParseModelDeprecation reads the payload of a 410 into what the dashboard
// shows next to the model. An unreadable payload yields an empty detail, which
// callers treat as "unknown", never as "no deprecation".
func ParseModelDeprecation(body []byte) DeprecationDetail {
	payload, ok := decodeDeprecation(body)
	if !ok {
		return DeprecationDetail{}
	}
	msg := payload.Message
	successor := ""
	if obj, isObject := deprecationObjectOf(payload.Error); isObject {
		msg = firstString(obj.Message, obj.Detail, payload.Message)
		successor = firstString(obj.Successor, obj.ReplacedBy, obj.Replacement)
	}
	if successor == "" {
		successor = successorFromMessage(msg)
	}
	return DeprecationDetail{Message: msg, Successor: successor}
}

// decodeDeprecation parses an error body, reporting false when it is not JSON.
func decodeDeprecation(body []byte) (deprecationErrorPayload, bool) {
	if len(body) == 0 {
		return deprecationErrorPayload{}, false
	}
	var payload deprecationErrorPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return deprecationErrorPayload{}, false
	}
	return payload, true
}

// deprecationObjectOf narrows the polymorphic `error` field to the OpenAI
// shape, reporting false when it is absent or spelled as a bare string.
func deprecationObjectOf(errValue any) (deprecationObject, bool) {
	if errValue == nil {
		return deprecationObject{}, false
	}
	encoded, err := json.Marshal(errValue)
	if err != nil {
		return deprecationObject{}, false
	}
	var obj deprecationObject
	//nolint:staticcheck // An already-decoded value is re-encoded by shape, not schema.
	if err := json.Unmarshal(encoded, &obj); err != nil {
		return deprecationObject{}, false
	}
	if obj.Type == "" && obj.Message == "" && obj.Code == nil && obj.Status == "" &&
		obj.Detail == "" && obj.Successor == "" && obj.ReplacedBy == "" && obj.Replacement == "" {
		return deprecationObject{}, false
	}
	return obj, true
}

// successorFromMessage pulls the replacement model out of an upstream
// message that names it inline ("mimo-v2.5-free is deprecated, use
// mimo-v2.6-flash-free instead"). Only an explicit "use X" reading is
// trusted, so a message that merely mentions another model cannot invent a
// successor.
func successorFromMessage(msg string) string {
	lower := strings.ToLower(msg)
	for _, lead := range []string{"use ", "try ", "migrate to ", "replaced by ", "replacement:"} {
		idx := strings.Index(lower, lead)
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(msg[idx+len(lead):])
		if candidate := strings.Trim(cutAtClauseEnd(rest), " \"'`"); candidate != "" && !strings.ContainsAny(candidate, " \t") {
			return candidate
		}
	}
	return ""
}

// cutAtClauseEnd trims the sentence tail that follows a replacement model
// ("mimo-v2.6-flash-free instead." -> "mimo-v2.6-flash-free").
//
// It cannot cut at a bare ".": model ids carry dots of their own, and doing
// so truncated the successor to "mimo-v2". A trailing clause marker ends the
// id; otherwise the id runs to the next comma or to the end of the string,
// which leaves a version in the id intact.
func cutAtClauseEnd(s string) string {
	for _, tail := range []string{" instead", " rather", " going forward", " now"} {
		if idx := strings.Index(strings.ToLower(s), tail); idx > 0 {
			return s[:idx]
		}
	}
	if cut := strings.Index(s, ", "); cut > 0 {
		return s[:cut]
	}
	return strings.TrimRight(s, ".,;:!?)]}\"'")
}

func firstString(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// deprecationCodeString renders an error `code`, which providers spell as
// either a string or a number.
func deprecationCodeString(code any) string {
	switch v := code.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case nil:
		return ""
	default:
		return ""
	}
}
