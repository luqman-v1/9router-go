package executor

import (
	"context"
	"strings"
	"testing"

	"9router/proxy/internal/translator"
)

// genUsageStream builds a chat.completion.chunk body whose frames carry no
// usage until the terminal chunk — the shape of a normal streamed turn.
func genUsageStream(n int) []byte {
	var b strings.Builder
	for range n {
		b.WriteString(`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"tok"}}]}` + "\n\n")
	}
	b.WriteString(`data: {"id":"chatcmpl-x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":128,"completion_tokens":64}}` + "\n\n")
	b.WriteString("data: [DONE]\n\n")
	return []byte(b.String())
}

// BenchmarkCaptureUsageGuarded measures captureUsageFromSSEChunk on one read's
// worth of content frames that carry no usage — the case the `"usage"`
// substring guard exists for: the whole nested ScanStream (a fresh 64KB
// scanner buffer per call) must be skipped, not paid per read.
func BenchmarkCaptureUsageGuarded(b *testing.B) {
	stream := genUsageStream(200)
	// Split into 4KB reads the way SSECopy's fixed buffer does.
	const readSize = 4096
	chunks := make([][]byte, 0, len(stream)/readSize+1)
	for off := 0; off < len(stream); off += readSize {
		end := off + readSize
		if end > len(stream) {
			end = len(stream)
		}
		chunks = append(chunks, stream[off:end])
	}
	ctx := context.Background()
	var acc *translator.OpenAIUsage
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for _, chunk := range chunks {
			acc = captureUsageFromSSEChunk(ctx, acc, chunk)
		}
	}
	_ = acc
}

// BenchmarkParseResponseUsageMiss measures the fallback cost when a chunk
// slips past the substring guard — e.g. a code-generation turn quoting
// `"usage"` as literal text — but carries no real usage object.
func BenchmarkParseResponseUsageMiss(b *testing.B) {
	payload := []byte(`{"id":"c","choices":[{"delta":{"content":"the \"usage\" field"}}]}`)
	b.ReportAllocs()
	for range b.N {
		_ = translator.ParseResponseUsage(payload)
	}
}
