package proxy

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// genSSE builds a realistic chat.completion.chunk stream: n content events,
// one usage-bearing terminal chunk, and [DONE]. Sizes mirror what an upstream
// actually streams — tens of bytes of JSON per event, one event per line pair.
func genSSE(n int) []byte {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b,
			`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"tok%d"},"finish_reason":null}]}`+"\n\n",
			i)
	}
	b.WriteString(`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":128,"completion_tokens":64}}` + "\n\n")
	b.WriteString("data: [DONE]\n\n")
	return []byte(b.String())
}

// BenchmarkSSECopyPassthrough measures the executor passthrough lane
// (SSECopy): raw read loop, line framing, and the per-read write+flush to the
// client. This is the hot path for providers whose format matches the client
// and needs no translation.
func BenchmarkSSECopyPassthrough(b *testing.B) {
	payload := genSSE(500)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rec := httptest.NewRecorder()
		if err := SSECopy(rec, bytes.NewReader(payload), rec, func([]byte) {}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkScanStreamParse measures the handler lane (ScanStream): full SSE
// event framing with multi-line and CRLF handling. The handler path pays this
// once per event for usage capture and translation hooks.
func BenchmarkScanStreamParse(b *testing.B) {
	payload := genSSE(500)
	sink := 0
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := ScanStream(bytes.NewReader(payload), func(p []byte) { sink += len(p) }); err != nil {
			b.Fatal(err)
		}
	}
	_ = sink
}

// BenchmarkScanStreamMultiLineData pins the slower event shape: a stream whose
// events split their `data:` payload across several lines, so emit takes the
// bytes.Join path instead of the single-line fast path. Exists so a later edit
// that regresses the fast path cannot silently leave the join path unmeasured.
func BenchmarkScanStreamMultiLineData(b *testing.B) {
	var bld strings.Builder
	for i := range 250 {
		fmt.Fprintf(&bld,
			"data: {\"id\":\"chatcmpl-x\",\n data: \"delta\":{\"content\":\"tok%d\"},\n data: \"done\":false}\n\n",
			i)
	}
	bld.WriteString("data: [DONE]\n\n")
	payload := []byte(bld.String())
	sink := 0
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := ScanStream(bytes.NewReader(payload), func(p []byte) { sink += len(p) }); err != nil {
			b.Fatal(err)
		}
	}
	_ = sink
}
