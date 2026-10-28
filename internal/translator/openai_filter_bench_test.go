package translator

import "testing"

// BenchmarkFilterToOpenAIFormatJSON measures the OpenAI filter the request
// path pays for on every request: a decode, a rewrite, and — only when the
// filter changed something — a re-serialisation. The number belongs in the
// changelog, and it has to be reproducible from the repository for a reviewer
// to check it.
func BenchmarkFilterToOpenAIFormatJSON(b *testing.B) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],` +
		`"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{}}}}]}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = FilterToOpenAIFormatJSON(body, FilterOptions{})
	}
}
