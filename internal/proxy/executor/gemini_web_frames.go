package executor

import (
	json "encoding/json/v2"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// A StreamGenerate response is Google's generic `batchexecute` line protocol,
// not a documented API: `)]}'` framing, a byte-count line before each chunk, and
// the chunk itself a JSON array of `["wrb.fr", <rpcid>, "<payload json>"]`
// envelopes. Everything in this file reads that wire form, and the shapes below
// were taken from live responses rather than from documentation — there is none.
const (
	// geminiWebOrigin is the site every request is addressed to. The executor
	// derives nothing from the connection's base URL: unlike chat.deepseek.com
	// there is no self-hosted sibling, so a configurable base URL could only
	// point somewhere the protocol does not exist.
	geminiWebOrigin = "https://gemini.google.com"

	// geminiWebAppURL is the page whose inline `WIZ_global_data` carries the
	// per-session tokens (`cfb2h`, `FdrFJe`) every later call echoes back.
	geminiWebAppURL = geminiWebOrigin + "/app"

	// geminiWebStreamPath is StreamGenerate. It is a POST of an
	// `application/x-www-form-urlencoded` body holding a nested JSON array;
	// the response is the line stream parsed below.
	geminiWebStreamPath = "/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate"

	// geminiWebBatchPath is the generic RPC transport, used here for model and
	// quota discovery.
	geminiWebBatchPath = "/_/BardChatUi/data/batchexecute"

	// geminiWebUserAgent is the Chrome build the request shape was captured
	// from. The site serves the same HTML to a plain Go client, so this is
	// consistency rather than impersonation.
	geminiWebUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"
)

// geminiWebModelIDRe matches the 16-hex-digit identity Gemini Web stamps on an
// answer. It is compared before being reported, so a payload that reuses that
// slot for something other than a model id cannot be misread as one.
var geminiWebModelIDRe = regexp.MustCompile(`^[0-9a-f]{14,16}$`)

// geminiWebCodeTraceRe matches the sandbox trace Gemini Web inlines in an
// answer — ```python?code_reference&code_event_index=1 … ``` and its
// `code_stdout` sibling. It is the model's working, not its answer, so it is
// moved to the reasoning channel rather than left in the content a coding
// client would read as the reply.
var geminiWebCodeTraceRe = regexp.MustCompile(
	"(?is)```[a-z0-9_+-]*\\?code_(?:reference|stdout|stderr)&code_event_index=\\d+[^\\n]*\\n([\\s\\S]*?)```\\s*")

// geminiWebFrame is one candidate inside a streamed line. `Text` is the whole
// answer so far and `Thoughts` the whole reasoning so far — Gemini Web re-sends
// both on every frame rather than sending deltas, which is why the callers
// derive deltas instead of forwarding text.
type geminiWebFrame struct {
	RCID     string
	Text     string
	Thoughts string
	Done     bool
}

// geminiWebLine is one parsed line of the StreamGenerate response.
type geminiWebLine struct {
	CID              string
	RID              string
	ContextToken     string
	ServedModelID    string
	ServedModelLabel string
	// Started marks the line that opens the answer.
	Started   bool
	StreamEnd bool
	Frames    []geminiWebFrame
	RPCError  *geminiWebRPCError
}

// geminiWebRPCError is a failure the upstream reported inside an otherwise
// HTTP-200 stream. The site uses this for the cases the router has to act on —
// quota exhausted, model invalid, session rejected — so collapsing them all
// into one 502 would cost the fallback layer the ability to rotate.
type geminiWebRPCError struct {
	Code    int
	Status  int
	Type    string
	Message string
	Raw     string
}

func (e *geminiWebRPCError) Error() string { return e.Message }

// jspbField reads a field from the positional-array form Google's protobuf-JSON
// serializer emits.
//
// Two encodings are in use for the same field: a dense array where the index is
// the position, and a sparse one where trailing fields collapse into a single
// object keyed by the 1-based field number. Live StreamGenerate payloads use the
// second for the tail fields this file reads (the context token and the served
// model identity are always in that trailing object), so a reader that only
// indexed the array would find nothing and report a healthy stream as empty.
func jspbField(arr []any, index int) any {
	if len(arr) == 0 {
		return nil
	}
	if index >= 0 && index < len(arr) && arr[index] != nil {
		val := arr[index]
		if index < len(arr)-1 {
			return val
		}
		if _, isMap := val.(map[string]any); !isMap {
			return val
		}
	}
	if lastMap, ok := arr[len(arr)-1].(map[string]any); ok {
		return lastMap[strconv.Itoa(index+1)]
	}
	return nil
}

// parseGeminiWebLine reads one line of the stream.
//
// A nil result with a nil error means the line carried no information — the
// `)]}'` prefix, a byte-count line, or a keep-alive. Those are normal, and
// treating them as malformed would fail a healthy stream.
func parseGeminiWebLine(line string) (*geminiWebLine, error) {
	s := strings.TrimSpace(line)
	if s == "" || s == ")]}'" || s == ")]}" || isDigits(s) {
		return nil, nil
	}
	var outer []any
	if err := json.Unmarshal([]byte(s), &outer); err != nil {
		return nil, fmt.Errorf("gemini-web: parse stream frame: %w", err)
	}

	result := &geminiWebLine{}
	for _, partAny := range outer {
		part, ok := partAny.([]any)
		if !ok || len(part) == 0 {
			continue
		}
		tag, _ := part[0].(string)
		switch tag {
		case "er":
			result.RPCError = geminiWebClassifyRPCError(extractGeminiWebErrCode(part), part)
			continue
		case "e":
			// The stream's own end marker. Gemini Web sends `e` after the last
			// frame, and it is the only signal that says the answer finished
			// rather than the connection dropping.
			result.StreamEnd = true
			continue
		case "wrb.fr":
		default:
			continue
		}
		if len(part) <= 2 || part[2] == nil {
			continue
		}
		payload, ok := part[2].(string)
		if !ok {
			continue
		}
		var pj []any
		if err := json.Unmarshal([]byte(payload), &pj); err != nil {
			return nil, fmt.Errorf("gemini-web: parse stream payload: %w", err)
		}
		result.readPayload(pj)
	}
	return result, nil
}

// readPayload copies everything one wrb.fr payload contributes to the line.
func (l *geminiWebLine) readPayload(pj []any) {
	if ids, ok := jspbField(pj, 1).([]any); ok {
		if len(ids) > 0 {
			if cid, ok := ids[0].(string); ok && cid != "" {
				l.CID = cid
			}
		}
		if len(ids) > 1 {
			if rid, ok := ids[1].(string); ok && rid != "" {
				l.RID = rid
			}
		}
	}
	if ctx, ok := jspbField(pj, 25).(string); ok && ctx != "" {
		l.ContextToken = ctx
	}
	if _, ok := jspbField(pj, 2).(map[string]any); ok {
		l.Started = true
	}
	if id, ok := jspbField(pj, 39).(string); ok && geminiWebModelIDRe.MatchString(id) {
		l.ServedModelID = id
		if label, ok := jspbField(pj, 42).(string); ok {
			l.ServedModelLabel = label
		}
	}
	candidates, ok := jspbField(pj, 4).([]any)
	if !ok {
		return
	}
	for _, candidateAny := range candidates {
		candidate, ok := candidateAny.([]any)
		if !ok {
			continue
		}
		frame := geminiWebFrame{}
		if len(candidate) > 0 {
			frame.RCID, _ = candidate[0].(string)
		}
		if len(candidate) > 1 {
			if parts, ok := candidate[1].([]any); ok && len(parts) > 0 {
				frame.Text, _ = parts[0].(string)
			}
		}
		if thoughts, ok := jspbField(candidate, 37).([]any); ok && len(thoughts) > 0 {
			if first, ok := thoughts[0].([]any); ok && len(first) > 0 {
				frame.Thoughts, _ = first[0].(string)
			}
		}
		if state, ok := jspbField(candidate, 8).([]any); ok && len(state) > 0 {
			if code, ok := state[0].(float64); ok && int(code) == 2 {
				frame.Done = true
			}
		}
		frame.Text, frame.Thoughts = sanitizeGeminiWebCandidate(frame.Text, frame.Thoughts)
		l.Frames = append(l.Frames, frame)
	}
}

// isDigits reports whether a line is the byte-count prefix the stream
// interleaves ahead of each chunk. It carries no payload.
func isDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// sanitizeGeminiWebCandidate separates the answer from the model's working.
//
// Gemini Web inlines its sandbox trace — fenced `?code_reference` and
// `?code_stdout` blocks — ahead of the answer it derived from them. Left in
// place it lands in the content channel, where a coding client reads a Python
// snippet and a stdout dump as the reply.
func sanitizeGeminiWebCandidate(text, thoughts string) (string, string) {
	if text == "" {
		return "", thoughts
	}
	if !strings.Contains(text, "?code_") {
		return text, thoughts
	}
	for _, match := range geminiWebCodeTraceRe.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 {
			continue
		}
		block := strings.TrimSpace(match[1])
		if block == "" {
			continue
		}
		if thoughts != "" {
			thoughts += "\n"
		}
		thoughts += block
	}
	return strings.TrimSpace(geminiWebCodeTraceRe.ReplaceAllString(text, "")), thoughts
}

// extractGeminiWebErrCode reads the numeric code out of an `er` envelope. The
// code is the last positive number in the entry, which is where every observed
// shape puts it.
func extractGeminiWebErrCode(entry []any) int {
	for i := len(entry) - 1; i >= 1; i-- {
		if f, ok := entry[i].(float64); ok && f > 0 {
			return int(f)
		}
	}
	return 0
}

// geminiWebClassifyRPCError maps an upstream RPC code onto the status the
// router acts on. A quota or rate code has to arrive as 429 so the connection is
// cooled down and the next one is tried; a rejected session has to arrive as 401
// so the credential is treated as stale rather than the request as malformed.
func geminiWebClassifyRPCError(code int, raw any) *geminiWebRPCError {
	encoded, _ := json.Marshal(raw)
	snippet := string(encoded)
	if len(snippet) > 200 {
		snippet = snippet[:200]
	}
	failure := &geminiWebRPCError{Code: code, Raw: snippet}
	switch code {
	case 1037, 1060:
		failure.Status, failure.Type = 429, "rate_limit_error"
		failure.Message = fmt.Sprintf("gemini-web upstream rate limit (code %d)", code)
	case 1050, 1052:
		failure.Status, failure.Type = 400, "model_invalid"
		failure.Message = fmt.Sprintf("gemini-web rejected the requested model (code %d)", code)
	case 1016:
		// The site's "unauthenticated" code. Anonymous turns do not carry a
		// credential, so this is the one code that does not mean what it says —
		// the caller decides, because only it knows whether one was sent.
		failure.Status, failure.Type = 401, "upstream_auth_error"
		failure.Message = "gemini-web session was refused (code 1016)"
	case 401, 403:
		failure.Status, failure.Type = code, "upstream_auth_error"
		failure.Message = fmt.Sprintf("gemini-web auth error %d: %s", code, snippet)
	default:
		failure.Status, failure.Type = 502, "upstream_error"
		failure.Message = fmt.Sprintf("gemini-web upstream error (code %d)", code)
	}
	return failure
}
