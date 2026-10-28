package chat

import (
	"context"
	"fmt"
	"net/http"

	"9router/proxy/internal/handlerutil"
)

// PingConnection sends one chat request to one provider connection.
//
// This is the auto-ping path (upstream src/shared/services/quotaAutoPing.js):
// a tiny request so the provider's 5h quota window starts on time instead of
// at the first real request. It is the ordinary chat lane with the
// connection pinned, so the credential resolution, the OAuth refresh, the
// proxy-pool binding and the provider executor are the ones real traffic
// uses.
//
// The context must already carry the probe marker (handlerutil.WithProbeContext):
// a ping is not operator traffic, and the probe marker is what keeps it out
// of the usage ledger and the dashboard's request list.
func (h *ChatHandler) PingConnection(ctx context.Context, provider, model, connectionID string, body []byte) error {
	if h == nil {
		return fmt.Errorf("no chat handler")
	}
	if connectionID == "" {
		return fmt.Errorf("no connection id")
	}
	if model == "" {
		return fmt.Errorf("no model")
	}

	ctx = handlerutil.WithProbeContext(ctx)

	// A discarded writer: the ping reads the upstream's verdict from the
	// error the forwarder returns, not from the body. The writer still has to
	// be a valid ResponseWriter because the forwarder writes to it.
	rec := &pingResponseWriter{}

	err := h.handleAccountFallback(ctx, rec, provider, model, connectionID, body, false, false, "/v1/chat/completions")
	if err != nil {
		return err
	}
	if rec.status >= 400 {
		return fmt.Errorf("ping %s/%s failed: HTTP %d", provider, connectionID, rec.status)
	}
	return nil
}

// pingResponseWriter swallows the ping's response body and remembers the
// status code.
//
// The upstream's answer to a one-token ping carries nothing an operator needs,
// and the forwarder writes it unconditionally — so the writer drops the bytes
// rather than buffering them.
type pingResponseWriter struct {
	status int
}

func (w *pingResponseWriter) Header() http.Header {
	// The forwarder sets a Content-Type before it writes; a throwaway map is
	// enough because nothing reads it back.
	return make(http.Header)
}

func (w *pingResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return len(b), nil
}

func (w *pingResponseWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}
