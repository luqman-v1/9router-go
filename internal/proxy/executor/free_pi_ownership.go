package executor

// free-pi's 200-body refusal detector.
//
// free-pi can refuse a request with HTTP 200 and the refusal INSIDE the body as
// ordinary `message.content`, with `finish_reason: "stop"` and zero usage. A
// status-only check is blind to that: the client receives a plausible-looking
// answer carrying an error message instead of an actionable failure.
//
// The shared `proxy.EmptyUpstreamError` guard cannot catch it either — its
// `carriesAnswer()` accepts any non-empty carrier, and a refusal is non-empty
// text. So this provider needs its own content check, placed ahead of the
// generic one in ForwardFreePi.
//
// Two signals are required together, never one alone:
//
//   - a marker phrase from the refusal family, and
//   - zero completion tokens in `usage`
//
// The second is what separates a refusal from a genuine answer about the same
// subject. A model that emitted the text spent tokens producing it, so
// non-empty content with zero completion tokens is structurally impossible for
// a real completion. Either signal on its own misfires: the wording can
// legitimately be quoted in an answer, and zero usage alone would flag genuine
// empty turns.

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// freePiConversationOwnershipRefusal is the family of 200-body refusals this
// detector answers.
//
// It is deliberately NOT the session-rotation fix: rotating the session id
// cannot change which ACCOUNT is asking. "Use one account per person" is an
// account-identity rule, and free-pi enforcing it is upstream's decision — so
// the honest outcome here is a real, actionable error the fallback layer can
// act on, never a disguised request.
type freePiConversationOwnershipRefusal struct {
	status  int
	message string
}

// freePiConversationOwnershipMarkers are the phrase families the refusal has
// been observed in. The wording is server-owned and has changed between
// deploys (the ad gate's own text was reworded once upstream), so this matches
// a family rather than one literal — but it always requires the usage signal
// below before it will fire.
var freePiConversationOwnershipMarkers = []string{
	"another free-pi account",
	"belongs to another",
	"rotating accounts isn't allowed",
	"rotating accounts is not allowed",
	"account rotation is not allowed",
	"one account per person",
}

// freePiOwnershipRefusalInBody reports whether a 200 body is a conversation
// ownership refusal rather than a completion.
//
// Both signals must hold: a marker phrase AND zero completion tokens.
func freePiOwnershipRefusalInBody(body []byte) (freePiConversationOwnershipRefusal, bool) {
	trimmed := bytes.TrimSpace(body)
	// An event stream is not this shape; the streaming path folds first.
	if len(trimmed) == 0 || trimmed[0] != '{' || proxy.LooksLikeSSE(trimmed) {
		return freePiConversationOwnershipRefusal{}, false
	}

	var probe freePiOwnershipProbe
	if err := json.Unmarshal(trimmed, &probe, handlerutil.UpstreamBody); err != nil {
		return freePiConversationOwnershipRefusal{}, false
	}
	if probe.completionTokens() != 0 {
		// Tokens were spent producing this text, so it is a real answer.
		return freePiConversationOwnershipRefusal{}, false
	}
	text := probe.contentText()
	if text == "" {
		return freePiConversationOwnershipRefusal{}, false
	}

	lower := strings.ToLower(text)
	for _, marker := range freePiConversationOwnershipMarkers {
		if strings.Contains(lower, marker) {
			return freePiConversationOwnershipRefusal{
				status:  http.StatusForbidden,
				message: freePiConversationOwnershipMessage(text),
			}, true
		}
	}
	return freePiConversationOwnershipRefusal{}, false
}

// freePiOwnershipProbe reads only the fields the detector needs: where the text
// lives across the three wire shapes, and the usage counter.
type freePiOwnershipProbe struct {
	Choices jsontext.Value `json:"choices"`
	Content jsontext.Value `json:"content"`
	Output  jsontext.Value `json:"output"`
	Usage   struct {
		CompletionTokens int `json:"completion_tokens"`
		// Claude Messages spells it output_tokens.
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// completionTokens is the spent-token count under either spelling.
func (p freePiOwnershipProbe) completionTokens() int {
	return p.Usage.CompletionTokens + p.Usage.OutputTokens
}

// contentText extracts the assistant text from whichever wire shape carried it.
func (p freePiOwnershipProbe) contentText() string {
	if len(p.Choices) > 0 {
		var choices []struct {
			Message struct {
				Content jsontext.Value `json:"content"`
			} `json:"message"`
			Delta struct {
				Content jsontext.Value `json:"content"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(p.Choices, &choices, handlerutil.UpstreamBody); err != nil {
			return ""
		}
		for _, c := range choices {
			if text := freePiDecodeContent(c.Message.Content); text != "" {
				return text
			}
			if text := freePiDecodeContent(c.Delta.Content); text != "" {
				return text
			}
		}
		return ""
	}
	if text := freePiDecodeContent(p.Content); text != "" {
		return text
	}
	// Responses shape: content parts under output[].
	var output []struct {
		Content []struct {
			Text jsontext.Value `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(p.Output, &output, handlerutil.UpstreamBody); err == nil {
		for _, item := range output {
			for _, part := range item.Content {
				if text := freePiDecodeContent(part.Text); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

// freePiDecodeContent decodes a content field that is either a plain string or
// an array of parts.
func freePiDecodeContent(raw jsontext.Value) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s, handlerutil.UpstreamBody); err == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts, handlerutil.UpstreamBody); err == nil {
		var b strings.Builder
		for _, part := range parts {
			b.WriteString(part.Text)
		}
		return b.String()
	}
	return ""
}

// freePiConversationOwnershipMessage keeps the refusal verbatim in the error
// envelope. The wording is upstream's and the operator needs to read exactly
// what was refused, not a paraphrase of it.
func freePiConversationOwnershipMessage(refusal string) string {
	return providers.FreePiConversationOwnershipPrefix + ": " + strings.TrimSpace(refusal)
}
