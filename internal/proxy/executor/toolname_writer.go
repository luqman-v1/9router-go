package executor

import (
	"net/http"

	"9router/proxy/internal/translator"
)

// toolNameRestoringWriter memulihkan nama tool asli pada respons yang ditulis ke
// klien. Dipakai jalur opencode: nama tool bawaan (Bash/Glob/Grep/Read)
// disamarkan jadi huruf kecil sebelum dikirim ke upstream agar gate free-tier
// lolos, lalu dikembalikan ke penulisan yang dikenal klien pada respons.
//
// Menangani dua bentuk sekaligus:
//   - streaming SSE (frame berisi "data: {...}")
//   - non-streaming JSON (satu body utuh)
//
// Embedding http.ResponseWriter mempertahankan Header()/WriteHeader(); Flush()
// didelegasikan supaya type-assertion http.Flusher di jalur SSE tetap berhasil.
type toolNameRestoringWriter struct {
	http.ResponseWriter
	toolNameMap map[string]string
}

// NewToolNameRestoringWriter membungkus w bila ada nama tool yang perlu
// dipulihkan; mengembalikan w apa adanya bila tidak ada.
func NewToolNameRestoringWriter(w http.ResponseWriter, toolNameMap map[string]string) http.ResponseWriter {
	if w == nil || len(toolNameMap) == 0 {
		return w
	}
	if _, already := w.(*toolNameRestoringWriter); already {
		return w
	}
	return &toolNameRestoringWriter{ResponseWriter: w, toolNameMap: toolNameMap}
}

func (w *toolNameRestoringWriter) Write(p []byte) (int, error) {
	restored := translator.RestoreToolNamesInPayload(p, w.toolNameMap)
	if _, err := w.ResponseWriter.Write(restored); err != nil {
		return 0, err
	}
	// Laporkan panjang asli supaya pemanggil tidak mengira terjadi short write
	// ketika penggantian nama mengubah ukuran frame.
	return len(p), nil
}

// Flush meneruskan ke writer dasar bila mendukung http.Flusher.
func (w *toolNameRestoringWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
