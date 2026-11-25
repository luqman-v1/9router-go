package chat

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

const maxFallbackAttempts = 10

type FallbackAttempt struct {
	ConnectionID string `json:"connectionId"`
	Account      string `json:"account"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	StatusCode   int    `json:"statusCode"`
	Error        string `json:"error,omitempty"`
	LatencyMs    int64  `json:"latencyMs"`
}

type FallbackAttemptRecorder struct {
	mu       sync.Mutex
	attempts []FallbackAttempt
}

func NewFallbackAttemptRecorder() *FallbackAttemptRecorder {
	return &FallbackAttemptRecorder{
		attempts: make([]FallbackAttempt, 0, maxFallbackAttempts),
	}
}

func (r *FallbackAttemptRecorder) Record(attempt FallbackAttempt) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.attempts) >= maxFallbackAttempts {
		return
	}
	r.attempts = append(r.attempts, attempt)
}

func (r *FallbackAttemptRecorder) Snapshot() []FallbackAttempt {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.attempts) == 0 {
		return nil
	}
	out := make([]FallbackAttempt, len(r.attempts))
	copy(out, r.attempts)
	return out
}

type fallbackAttemptRecorderContextKey struct{}

func WithFallbackAttemptRecorder(ctx context.Context, recorder *FallbackAttemptRecorder) context.Context {
	return context.WithValue(ctx, fallbackAttemptRecorderContextKey{}, recorder)
}

func FallbackAttemptRecorderFrom(ctx context.Context) *FallbackAttemptRecorder {
	if ctx == nil {
		return nil
	}
	rec, _ := ctx.Value(fallbackAttemptRecorderContextKey{}).(*FallbackAttemptRecorder)
	return rec
}

func RecordAttemptFromContext(ctx context.Context, connID, account, provider, model string, statusCode int, err error, latencyMs int64) {
	recorder := FallbackAttemptRecorderFrom(ctx)
	if recorder == nil {
		return
	}

	if statusCode == 0 && err != nil {
		if code, ok := upstreamStatusCode(err); ok {
			statusCode = code
		} else {
			statusCode = 500
		}
	}

	attempt := FallbackAttempt{
		ConnectionID: connID,
		Account:      account,
		Provider:     provider,
		Model:        model,
		StatusCode:   statusCode,
		LatencyMs:    latencyMs,
	}

	if err != nil {
		text := ""
		var ue *upstreamError
		if errors.As(err, &ue) && len(ue.Body) > 0 {
			if parsed := extractErrorText(ue.Body); parsed != "" {
				text = parsed
			} else if body := strings.TrimSpace(string(ue.Body)); body != "" {
				// Some upstreams (Cloudflare Workers AI among them) answer with a
				// body shape extractErrorText does not recognise. The wrapper's
				// "forward to <url>" prefix alone is ~160 runes, so falling back to
				// err.Error() used to truncate the useful part away entirely. Keep
				// the raw body and let the status code travel in StatusCode.
				text = fmt.Sprintf("upstream returned %d: %s", ue.StatusCode, body)
			}
		}
		if text == "" {
			text = err.Error()
		}
		text = truncateDetailText(text, maxPersistedErrorLen, "...[truncated]")
		attempt.Error = text
	}

	recorder.Record(attempt)
}

func upstreamStatusCode(err error) (int, bool) {
	for err != nil {
		value := reflect.ValueOf(err)
		if value.Kind() == reflect.Pointer && !value.IsNil() {
			value = value.Elem()
		}
		if value.Kind() == reflect.Struct {
			if field := value.FieldByName("StatusCode"); field.IsValid() && field.Kind() == reflect.Int {
				return int(field.Int()), true
			}
		}

		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = unwrapper.Unwrap()
	}
	return 0, false
}

var _ = errors.New
