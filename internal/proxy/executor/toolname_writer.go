package executor

import (
	"net/http"

	"9router/proxy/internal/translator"
)

// toolNameRestoringWriter restores the caller's original tool names on every
// response written to the client. It serves the opencode paths, where the
// built-in tools (Bash/Glob/Grep/Read) are renamed to lowercase before being
// sent upstream so the free-tier gate accepts the request, and must therefore be
// put back on the way out.
//
// Both shapes are handled: streaming SSE frames (lines carrying "data: {...}")
// and a single non-streaming JSON body.
//
// Embedding http.ResponseWriter preserves Header()/WriteHeader(); Flush() is
// delegated so the http.Flusher type assertion used by the SSE paths still
// succeeds.
type toolNameRestoringWriter struct {
	http.ResponseWriter
	toolNameMap map[string]string
}

// NewToolNameRestoringWriter wraps w when there are tool names to restore, and
// returns w unchanged otherwise.
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
	// Report the original length so callers do not mistake a size change caused
	// by name restoration for a short write.
	return len(p), nil
}

// Flush forwards to the underlying writer when it supports http.Flusher.
func (w *toolNameRestoringWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
