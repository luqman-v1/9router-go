package executor

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"9router/proxy/internal/proxy"
)

// Qoder model-queue handling — port of open-sse/shared/qoder/queue.js,
// reverse-engineered from qodercli 1.1.41.
//
// When a model is temporarily out of capacity Qoder answers the inference
// request with a `{statusCodeValue, body}` SSE envelope whose body carries:
//
//	{ "code": "10605",
//	  "message": "{\"isQueued\":true,\"modelKey\":\"auto\",\"queueCount\":0,
//	               \"queueType\":\"p3\",\"retryAfterSeconds\":30,
//	               \"serviceAvailable\":false,\"subCode\":\"RESOURCE_LIMIT\",
//	               \"waitTime\":30}",
//	  "subCode": "RESOURCE_LIMIT" }
//
// That is a transient capacity signal, not a quota block: the model becomes
// available again in ~30s and the same request then succeeds. qodercli keeps
// the request open, polls the model-queue status endpoint, and replays the
// inference once the model is ready.
//
// The payload is frequently double- or triple-encoded (the queue object lives
// inside a JSON string inside a JSON string), so every lookup walks the nested
// tree and parses JSON-encoded string fields as it goes.

// QoderQueueCode marks the model-queue throttle.
const QoderQueueCode = "10605"

// QoderDeriveMachineID derives a stable machine id from the Qoder user id.
// qodercli generates one machine id per install and reuses it for every
// request (including queue-status polls and model-queue replays); the server
// tracks per-client queue state by it. Connections without a stored machine
// id therefore derive one instead of inventing a random uuid per request,
// which made each call a brand-new device and sent it to the back of the
// queue. Port of upstream deriveMachineId (shared/qoder/cosy.js).
func QoderDeriveMachineID(seed string) string {
	if seed == "" {
		return uuid.NewString()
	}
	sum := sha256.Sum256([]byte("qoder-machine\x00" + seed))
	h := hex.EncodeToString(sum[:])
	// Format as a UUID so it is indistinguishable from a generated machine id.
	return strings.Join([]string{
		h[0:8],
		h[8:12],
		"5" + h[13:16],
		"a" + h[17:20],
		h[20:32],
	}, "-")
}

// QoderModelQueueMaxWaitEnv raises the queue wait budget for callers that can
// afford to wait.
const QoderModelQueueMaxWaitEnv = "QODER_MODEL_QUEUE_MAX_WAIT_MS"

const (
	// qoderQueueMaxWaitDefault: the queue clears in ~30s windows, so 10
	// minutes covers several of them and still returns a retryable error
	// instead of hanging (qodercli itself caps at 1h, far too long for a
	// gateway holding a client request).
	qoderQueueMaxWaitDefault = 600_000
	qoderQueueRetryDefault   = 30_000
	// A sub-second floor came from qodercli, but Qoder's queue now answers
	// retryAfterSeconds:2 while still refusing the request. Replaying that
	// fast makes the throttle worse, so the floor is a full retry interval.
	qoderQueueRetryMin = 15_000
	qoderQueueRetryMax = 60_000
	// The queue-status endpoint can report isQueued:false while the inference
	// call is still refused, so its hint is not trusted below the default
	// interval.
	qoderQueueStatusHintFloor = 30_000
)

// qoderQueueInfo is a normalized queue descriptor parsed out of an inference
// refusal.
type qoderQueueInfo struct {
	Code             string
	SubCode          string
	IsQueued         *bool
	ModelKey         string
	QueueType        string
	QueueCount       *float64
	ServiceAvailable *bool
	RetryAfterSec    *float64
	WaitTime         *float64
}

// qoderQueueNestedKeys are the envelope keys the queue object hides behind.
var qoderQueueNestedKeys = []string{"error", "data", "body", "message", "cause", "result", "payload"}

var qoderNumberShape = regexp.MustCompile(`^-?\d+(?:\.\d+)?$`)

func qoderTryParse(value string) any {
	s := strings.TrimSpace(value)
	if s == "" || (s[0] != '{' && s[0] != '[') {
		return nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		return nil
	}
	return parsed
}

// qoderWalkObjects walks nested objects and JSON-encoded string fields
// breadth-first, returning every plain object reachable from root. Parsed
// JSON is acyclic by construction, so the walk terminates on its own; the
// object cap only bounds the work a pathological payload could force.
func qoderWalkObjects(root any) []map[string]any {
	const maxObjects = 512
	var out []map[string]any
	seenStrings := map[string]bool{}
	type holder struct {
		obj  map[string]any
		list []any
	}
	queue := []holder{{obj: asObject(root), list: asArray(root)}}
	for len(queue) > 0 && len(out) < maxObjects {
		node := queue[0]
		queue = queue[1:]
		if node.obj != nil {
			out = append(out, node.obj)
			for _, key := range qoderQueueNestedKeys {
				v, ok := node.obj[key]
				if !ok {
					continue
				}
				if asObject(v) != nil || asArray(v) != nil {
					queue = append(queue, holder{obj: asObject(v), list: asArray(v)})
					continue
				}
				if s, ok := v.(string); ok && !seenStrings[s] {
					seenStrings[s] = true
					if parsed := qoderTryParse(s); parsed != nil {
						queue = append(queue, holder{obj: asObject(parsed), list: asArray(parsed)})
					}
				}
			}
			continue
		}
		for _, item := range node.list {
			if asObject(item) != nil || asArray(item) != nil {
				queue = append(queue, holder{obj: asObject(item), list: asArray(item)})
			}
		}
	}
	return out
}

func asObject(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return m
}

func asArray(v any) []any {
	a, ok := v.([]any)
	if !ok {
		return nil
	}
	return a
}

func qoderInfoString(obj map[string]any, key string, current string) string {
	if current != "" {
		return current
	}
	if s, ok := obj[key].(string); ok {
		if t := strings.TrimSpace(s); t != "" {
			return t
		}
	}
	return current
}

func qoderInfoBool(obj map[string]any, key string, current *bool) *bool {
	if current != nil {
		return current
	}
	switch v := obj[key].(type) {
	case bool:
		return &v
	case string:
		if v == "true" {
			t := true
			return &t
		}
		if v == "false" {
			f := false
			return &f
		}
	}
	return current
}

func qoderInfoNumber(obj map[string]any, key string, current *float64) *float64 {
	if current != nil {
		return current
	}
	switch v := obj[key].(type) {
	case float64:
		return &v
	case string:
		if qoderNumberShape.MatchString(strings.TrimSpace(v)) {
			if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return &n
			}
		}
	}
	return current
}

// ParseQoderQueueInfo parses a queue payload into a normalized descriptor.
// Input may be a raw string, a parsed object, or an SSE envelope body. Returns
// nil unless the payload actually carries a queue signal — a plain
// `code: "10605"` without the inner object still counts, because the code
// alone is the signal the executor keys off.
func ParseQoderQueueInfo(input any) *qoderQueueInfo {
	root := input
	if s, ok := input.(string); ok {
		root = qoderTryParse(s)
		if root == nil {
			return nil
		}
	}
	if root == nil {
		return nil
	}

	info := &qoderQueueInfo{}
	for _, obj := range qoderWalkObjects(root) {
		info.Code = qoderInfoString(obj, "code", info.Code)
		info.SubCode = qoderInfoString(obj, "subCode", info.SubCode)
		info.IsQueued = qoderInfoBool(obj, "isQueued", info.IsQueued)
		info.ModelKey = qoderInfoString(obj, "modelKey", info.ModelKey)
		info.QueueType = qoderInfoString(obj, "queueType", info.QueueType)
		info.QueueCount = qoderInfoNumber(obj, "queueCount", info.QueueCount)
		info.ServiceAvailable = qoderInfoBool(obj, "serviceAvailable", info.ServiceAvailable)
		info.RetryAfterSec = qoderInfoNumber(obj, "retryAfterSeconds", info.RetryAfterSec)
		info.WaitTime = qoderInfoNumber(obj, "waitTime", info.WaitTime)
	}

	queued := (info.IsQueued != nil && *info.IsQueued) ||
		(info.ServiceAvailable != nil && !*info.ServiceAvailable) ||
		info.SubCode == "RESOURCE_LIMIT" ||
		info.Code == QoderQueueCode
	if !queued {
		return nil
	}
	return info
}

// computeQoderQueueDelayMs picks the delay before the next attempt. Two
// sources feed this and they are not equally trustworthy: the inference
// refusal carries waitTime/retryAfterSeconds for the queue the request
// actually sits in, so its hint is honoured; the queue-status endpoint has
// been observed answering isQueued:false, retryAfterSeconds:2 while the
// inference call is still refused, so a status-derived delay never drops
// below qoderQueueStatusHintFloor. Both are clamped into
// qoderQueueRetryMin..qoderQueueRetryMax.
func computeQoderQueueDelayMs(info *qoderQueueInfo, fromStatus bool) int {
	hint := info.RetryAfterSec
	if hint == nil {
		hint = info.WaitTime
	}
	ms := float64(qoderQueueRetryDefault)
	if hint != nil {
		ms = *hint * 1000
	}
	if fromStatus {
		ms = max(ms, qoderQueueStatusHintFloor)
	}
	return int(min(qoderQueueRetryMax, max(qoderQueueRetryMin, ms)))
}

// qoderQueueMaxWaitMs is the total budget for holding a request open while
// its model is queued.
func qoderQueueMaxWaitMs() int {
	if raw, err := strconv.Atoi(strings.TrimSpace(os.Getenv(QoderModelQueueMaxWaitEnv))); err == nil && raw > 0 {
		return raw
	}
	return qoderQueueMaxWaitDefault
}

// qoderQueueStatusURL is the queue-status endpoint qodercli polls. All three
// parameters are required — omitting one answers HTTP 400.
func qoderQueueStatusURL(baseURL, requestSetID, modelKey, queueType string) string {
	params := url.Values{}
	if requestSetID != "" {
		params.Set("requestSetId", requestSetID)
	}
	if modelKey != "" {
		params.Set("modelKey", modelKey)
	}
	if queueType != "" {
		params.Set("queueType", queueType)
	}
	return fmt.Sprintf("%s/algo/api/v2/service/ask/queue/status?%s", strings.TrimRight(baseURL, "/"), params.Encode())
}

// qoderQueueStatus is a parsed queue-status response.
type qoderQueueStatus struct {
	IsQueued         *bool
	ServiceAvailable *bool
	ModelKey         string
	QueueType        string
	QueueCount       *float64
	RetryAfterSec    *float64
	WaitTime         *float64
}

// parseQoderQueueStatus parses the queue-status response. Returns nil when
// the body is not the expected shape, which the caller treats as "status
// endpoint unusable" and falls back to timed replays.
func parseQoderQueueStatus(input any) *qoderQueueStatus {
	root := input
	if s, ok := input.(string); ok {
		root = qoderTryParse(s)
	}
	obj := asObject(root)
	if obj == nil {
		return nil
	}
	isQueued := qoderInfoBool(obj, "isQueued", nil)
	if isQueued == nil {
		return nil
	}
	return &qoderQueueStatus{
		IsQueued:         isQueued,
		ServiceAvailable: qoderInfoBool(obj, "serviceAvailable", nil),
		ModelKey:         qoderInfoString(obj, "modelKey", ""),
		QueueType:        qoderInfoString(obj, "queueType", ""),
		QueueCount:       qoderInfoNumber(obj, "queueCount", nil),
		RetryAfterSec:    qoderInfoNumber(obj, "retryAfterSeconds", nil),
		WaitTime:         qoderInfoNumber(obj, "waitTime", nil),
	}
}

// qoderEnvelopeFirstFrame parses one SSE data payload as a Qoder envelope.
func qoderEnvelopeFirstFrame(payload []byte) (qoderEnvelope, bool) {
	var envelope qoderEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return envelope, false
	}
	return envelope, true
}

// qoderQueueInfoFromEnvelope extracts the queue signal from an envelope body
// (a JSON string inside the envelope's body field).
func qoderQueueInfoFromEnvelope(body jsontext.Value) *qoderQueueInfo {
	inner := qoderEnvelopeBody(body)
	if inner == "" {
		return nil
	}
	return ParseQoderQueueInfo(inner)
}

// qoderPeekResult is the first frame of an inference response, read without
// consuming the stream: a queued model announces itself there, and reading to
// completion would lose the ability to hand the same response back when it
// turns out to be a normal completion.
type qoderPeekResult struct {
	envelope qoderEnvelope
	parsed   bool
	consumed []byte // every byte read so far, for the hand-back
	reader   io.Reader
}

// qoderPeekFirstFrame reads upstream until the first complete SSE data frame
// and parses it as a Qoder envelope. The returned reader continues from where
// the peek stopped (buffered bytes included), so the caller can replay the
// consumed head plus the remainder as one intact stream.
func qoderPeekFirstFrame(r io.Reader) (qoderPeekResult, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	out := qoderPeekResult{reader: br}
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			out.consumed = append(out.consumed, line...)
			trimmed := strings.TrimSpace(string(line))
			if strings.HasPrefix(trimmed, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				if payload != "" && payload != "[DONE]" {
					if envelope, ok := qoderEnvelopeFirstFrame([]byte(payload)); ok {
						out.envelope = envelope
						out.parsed = true
					}
					return out, nil
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return out, nil
			}
			return out, fmt.Errorf("qoder stream peek: %w", err)
		}
	}
}

// qoderQueueStatusQuery carries the correlation ids the status poll needs;
// qodercli correlates the poll with the original inference request.
type qoderQueueStatusQuery struct {
	ModelKey     string
	QueueType    string
	RequestSetID string
	RequestID    string
	SessionID    string
}

// qoderFetchQueueStatus polls the model-queue status endpoint. The bool is
// false when the endpoint is missing, erroring, or answering an unknown
// shape, which the caller treats as "fall back to timed replays".
func qoderFetchQueueStatus(ctx context.Context, client *http.Client, chatTargetURL string, creds QoderCosyCreds, q qoderQueueStatusQuery) (*qoderQueueStatus, bool) {
	base, err := url.Parse(chatTargetURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, false
	}
	statusURL := qoderQueueStatusURL(base.Scheme+"://"+base.Host, q.RequestSetID, q.ModelKey, q.QueueType)

	headers, err := buildQoderCosyHeaders(nil, statusURL, creds)
	if err != nil {
		return nil, false
	}
	headers["Accept"] = "application/json"
	// gzip triggers signature validation on Qoder's CDN; force identity.
	headers["Accept-Encoding"] = "identity"
	headers["X-Request-ID"] = q.RequestID
	headers["X-Session-ID"] = q.SessionID

	pollCtx, cancel := context.WithTimeout(ctx, qoderCatalogTimeout)
	defer cancel()
	resp, err := proxy.DoRequest(pollCtx, client, http.MethodGet, statusURL, headers, nil)
	if err != nil {
		return nil, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, qoderMaxCatalogBytes))
	if err != nil {
		return nil, false
	}
	info := parseQoderQueueStatus(string(body))
	if info == nil {
		return nil, false
	}
	return info, true
}
