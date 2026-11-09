package chat

import (
	"strings"
	"testing"
)

// chatBodyForBench builds a Chat Completions body whose size and shape match
// what the request path actually carries: a conversation of n assistant turns
// and one token-limit field. It deliberately does NOT carry
// max_completion_tokens — the fast path is about the majority of requests,
// which name one spelling or none.
func chatBodyForBench(n int) []byte {
	turn := `{"role":"assistant","content":"` + strings.Repeat("agent output text ", 100) + `"}`
	var sb strings.Builder
	sb.WriteString(`{"model":"gpt-6-luna","messages":[`)
	for i := range n {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(turn)
	}
	sb.WriteString(`],"max_tokens":1024,"stream":true}`)
	return []byte(sb.String())
}

// BenchmarkNormalizeChatTokenFields measures the guard against the parse it
// skips. The number belongs in the changelog, and it has to be reproducible
// from the repository for a reviewer to check it.
func BenchmarkNormalizeChatTokenFields(b *testing.B) {
	sizes := []struct {
		name string
		n    int
	}{
		{"1KB", 1},
		{"9KB", 5},
		{"35KB", 20},
		{"89KB", 50},
	}
	for _, s := range sizes {
		body := chatBodyForBench(s.n)
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				_ = normalizeChatTokenFields(body)
			}
		})
	}
}

// BenchmarkNormalizeChatTokenFieldsBothFields is the case the guard cannot
// skip: the body names both spellings, so the scan succeeds and the parse runs
// anyway. It exists so the cost of the guard on the lane it does not help is a
// measured number rather than an assumption.
func BenchmarkNormalizeChatTokenFieldsBothFields(b *testing.B) {
	body := []byte(`{"model":"gpt-6-luna","messages":[{"role":"user","content":"hi"}],` +
		`"max_tokens":1024,"max_completion_tokens":2048,"stream":true}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		_ = normalizeChatTokenFields(body)
	}
}
