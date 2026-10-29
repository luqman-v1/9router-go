package executor

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"strings"
	"testing"
	"time"
)

func TestParseQoderQueueInfo(t *testing.T) {
	// The queue object is frequently double-encoded: JSON inside a JSON
	// string inside the envelope body.
	doubleEncoded := `{"code":"10605","message":"{\"isQueued\":true,\"modelKey\":\"auto\",\"queueCount\":0,\"queueType\":\"p3\",\"retryAfterSeconds\":30,\"serviceAvailable\":false,\"subCode\":\"RESOURCE_LIMIT\",\"waitTime\":30}","subCode":"RESOURCE_LIMIT"}`
	info := ParseQoderQueueInfo(doubleEncoded)
	if info == nil {
		t.Fatal("queue signal not recognized in a double-encoded payload")
	}
	if info.Code != "10605" || info.SubCode != "RESOURCE_LIMIT" || info.QueueType != "p3" || info.ModelKey != "auto" {
		t.Errorf("descriptor = %+v", info)
	}
	if info.IsQueued == nil || !*info.IsQueued || info.ServiceAvailable == nil || *info.ServiceAvailable {
		t.Errorf("flags = %+v", info)
	}
	if info.RetryAfterSec == nil || *info.RetryAfterSec != 30 {
		t.Errorf("retryAfterSeconds = %+v", info.RetryAfterSec)
	}

	// The bare code alone is the signal the executor keys off.
	if info := ParseQoderQueueInfo(`{"code":"10605","message":"busy"}`); info == nil {
		t.Error("bare 10605 code not recognized")
	}
	// Every flag independently triggers the queue path.
	for _, payload := range []string{
		`{"isQueued":true}`,
		`{"serviceAvailable":false}`,
		`{"subCode":"RESOURCE_LIMIT"}`,
	} {
		if ParseQoderQueueInfo(payload) == nil {
			t.Errorf("signal not recognized: %s", payload)
		}
	}
	// Billing and malformed requests are not queue signals.
	for _, payload := range []string{
		`{"code":"112","message":"quota exhausted"}`,
		`{"message":"first message is not system prompt"}`,
		`not json`,
		``,
	} {
		if ParseQoderQueueInfo(payload) != nil {
			t.Errorf("non-queue payload classified as queued: %s", payload)
		}
	}
}

func TestParseQoderQueueInfo_EnvelopeBody(t *testing.T) {
	envelope := qoderEnvelope{StatusCodeValue: 200}
	body := `{"code":"10605","message":"{\"isQueued\":true,\"retryAfterSeconds\":15}"}`
	// The envelope carries the inner object as an escaped JSON string.
	escaped, _ := json.Marshal(body)
	envelope.Body = jsontext.Value(escaped)
	info := qoderQueueInfoFromEnvelope(envelope.Body)
	if info == nil || info.RetryAfterSec == nil || *info.RetryAfterSec != 15 {
		t.Errorf("envelope body queue info = %+v", info)
	}
}

func TestComputeQoderQueueDelay(t *testing.T) {
	delay := computeQoderQueueDelayMs(&qoderQueueInfo{RetryAfterSec: ptrFloat(2)}, false)
	if delay != qoderQueueRetryMin {
		t.Errorf("sub-floor hint = %d, want clamped to %d", delay, qoderQueueRetryMin)
	}
	if got := computeQoderQueueDelayMs(&qoderQueueInfo{RetryAfterSec: ptrFloat(45)}, false); got != 45000 {
		t.Errorf("honoured hint = %d, want 45000", got)
	}
	// A status-derived delay never trusts a hint below the floor: the
	// endpoint has been observed answering retryAfterSeconds:2 while the
	// inference call is still refused.
	if got := computeQoderQueueDelayMs(&qoderQueueInfo{RetryAfterSec: ptrFloat(2)}, true); got != qoderQueueStatusHintFloor {
		t.Errorf("status hint = %d, want floor %d", got, qoderQueueStatusHintFloor)
	}
	if got := computeQoderQueueDelayMs(&qoderQueueInfo{}, false); got != qoderQueueRetryDefault {
		t.Errorf("no hint = %d, want default %d", got, qoderQueueRetryDefault)
	}
}

func TestParseQoderQueueStatus(t *testing.T) {
	ok := parseQoderQueueStatus(`{"isQueued":false,"queueCount":0,"retryAfterSeconds":2}`)
	if ok == nil || ok.IsQueued == nil || *ok.IsQueued {
		t.Errorf("ready status parsed as %+v", ok)
	}
	if parseQoderQueueStatus(`{"message":"unknown shape"}`) != nil {
		t.Error("unknown shape accepted")
	}
}

func TestQoderQueueStatusURL(t *testing.T) {
	got := qoderQueueStatusURL("https://api3.qoder.sh", "rs-1", "smodel", "p3")
	want := "https://api3.qoder.sh/algo/api/v2/service/ask/queue/status?modelKey=smodel&queueType=p3&requestSetId=rs-1"
	if got != want {
		t.Errorf("status url = %s, want %s", got, want)
	}
}

func TestQoderDeriveMachineID(t *testing.T) {
	a := QoderDeriveMachineID("user-1")
	b := QoderDeriveMachineID("user-1")
	if a != b {
		t.Error("derived machine id is not stable")
	}
	if a == QoderDeriveMachineID("user-2") {
		t.Error("different users share a machine id")
	}
	// UUID-shaped, so it is indistinguishable from a generated one.
	if len(a) != 36 || strings.Count(a, "-") != 4 || a[14] != '5' || a[19] != 'a' {
		t.Errorf("derived id %q is not uuid-shaped with the version/variant stamps", a)
	}
}

func TestQoderStaticModelConfig(t *testing.T) {
	cfg := qoderStaticModelConfig("smodel")
	if cfg == nil || cfg["display_name"] != "Sonus" || cfg["is_vl"] != true || cfg["is_reasoning"] != true {
		t.Fatalf("static config = %v", cfg)
	}
	// A fresh copy per call: the caller mutates model_config when applying a
	// context tier.
	cfg["key"] = "mutated"
	if qoderStaticModelConfig("smodel")["key"] != "smodel" {
		t.Error("static config map is shared across calls")
	}
	if qoderStaticModelConfig("unknown-model") != nil {
		t.Error("unknown key resolved a static config")
	}
}

// A queued model announces itself on the first frame; the peek must hand the
// consumed head back so the completion replays without a second upstream call.
func TestQoderPeekFirstFrame(t *testing.T) {
	stream := "data: " + `{"statusCodeValue":200,"statusCode":"OK","body":"{\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}"}` + "\n\n" +
		"data: [DONE]\n\n"
	peek, err := qoderPeekFirstFrame(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if !peek.parsed || peek.envelope.StatusCodeValue != 200 {
		t.Fatalf("first frame = %+v parsed=%v", peek.envelope, peek.parsed)
	}
	rest, rerr := io.ReadAll(peek.reader)
	if rerr != nil {
		t.Fatalf("read rest: %v", rerr)
	}
	// The remainder continues exactly where the peek stopped.
	full := string(peek.consumed) + string(rest)
	if !strings.Contains(full, "[DONE]") {
		t.Errorf("remainder lost: %q", full)
	}
	if !strings.HasPrefix(full, "data: {\"statusCodeValue\":200") {
		t.Errorf("consumed head mangled: %q", full[:40])
	}
}

func TestQoderPeekFirstFrame_QueueEnvelope(t *testing.T) {
	inner := `{"code":"10605","message":"{\"isQueued\":true,\"queueType\":\"p3\",\"retryAfterSeconds\":30,\"subCode\":\"RESOURCE_LIMIT\"}","subCode":"RESOURCE_LIMIT"}`
	escaped, _ := json.Marshal(inner)
	stream := "data: " + `{"statusCodeValue":200,"body":` + string(escaped) + `}` + "\n\n"
	peek, err := qoderPeekFirstFrame(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if !peek.parsed {
		t.Fatal("envelope not parsed")
	}
	info := qoderQueueInfoFromEnvelope(peek.envelope.Body)
	if info == nil {
		t.Fatal("queue signal not recognized")
	}
	if info.QueueType != "p3" {
		t.Errorf("queueType = %q", info.QueueType)
	}
}

func TestQoderSleepContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := qoderSleep(ctx, 60000); err == nil {
		t.Error("cancelled context did not abort the wait")
	}
	if time.Since(start) > time.Second {
		t.Error("cancelled wait did not return promptly")
	}
}

func ptrFloat(v float64) *float64 { return &v }
