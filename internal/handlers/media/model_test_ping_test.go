package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/handlerutil"
)

func TestProbeContextFlag(t *testing.T) {
	bg := context.Background()
	if handlerutil.IsProbeContext(bg) {
		t.Error("expected background context to not be probe context")
	}

	probeCtx := handlerutil.WithProbeContext(bg)
	if !handlerutil.IsProbeContext(probeCtx) {
		t.Error("expected probe context to report true")
	}
}

func TestReadProbeResult_BlockedCooldownResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusBadGateway)
	rec.Write([]byte(`{"error":{"message":"no available connections for provider: deepseek (all in cooldown, earliest reset 2026-10-09T01:00:00Z)","code":502}}`))

	res := readProbeResult(rec, "")
	if res.OK {
		t.Error("expected OK to be false for 502")
	}
	if !res.Blocked {
		t.Error("expected Blocked to be true for all in cooldown")
	}
	if res.ResetAt != "2026-10-09T01:00:00Z" {
		t.Errorf("ResetAt = %q, want 2026-10-09T01:00:00Z", res.ResetAt)
	}
}

func TestReadProbeResult_StandardErrorIsNotBlocked(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusInternalServerError)
	rec.Write([]byte(`{"error":{"message":"upstream error: internal failure"}}`))

	res := readProbeResult(rec, "")
	if res.OK {
		t.Error("expected OK to be false")
	}
	if res.Blocked {
		t.Error("expected Blocked to be false for standard 500 error")
	}
	if res.ResetAt != "" {
		t.Errorf("expected empty ResetAt, got: %q", res.ResetAt)
	}
}
