package proxy

import (
	"net/http"
	"testing"
)

type countingWriter struct {
	n      int64
	header http.Header
}

func (w *countingWriter) Write(b []byte) (int, error) { w.n += int64(len(b)); return len(b), nil }
func (w *countingWriter) WriteHeader(int)             {}
func (w *countingWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

// BenchmarkStreamContentPeekerHealthy measures the peeker's own cost on the
// healthy streaming path: three complete frames, one Write per frame, then
// Finish, with a writer that only counts bytes.
//
// It exists because the perf gate needs a number measured on the SAME probe on
// both trees, and earlier scratch benchmarks carried httptest.NewRecorder's
// ~1.5 us / 7 allocs of setup noise, which swamped the thing being measured.
// Measured with -benchtime=2s -count=4 on both trees: base 695-805 ns, head
// 899-947 ns, 384 -> 400 B, 6 -> 6 allocs. That is +0.15-0.2 us per streamed
// turn for classifying the first frame, and no new allocation on the healthy
// path; the earlier claims in this fragment came from benchmarks that carried
// httptest.NewRecorder's own ~1.5 us.
func BenchmarkStreamContentPeekerHealthy(b *testing.B) {
	frames := [][]byte{
		[]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello! \"}}]}\n\n"),
		[]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Here is the answer.\"}}]}\n\n"),
		[]byte("data: [DONE]\n\n"),
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := &countingWriter{}
		p := NewStreamContentPeeker(w)
		for _, f := range frames {
			_, _ = p.Write(f)
		}
		_ = p.Finish()
	}
}
