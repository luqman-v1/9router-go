package errclass

import (
	"net/http"
	"testing"
)

// TestClassifyNVIDIMessage pins the nvidia classification path from
// decolua@2275bc0d: an NIM cluster that reports "degraded" names one model's
// backend, not the credential, so the verdict must be model-scoped. A 400 frame
// whose prose is read as a request fault would leave the caller with
// ScopeRequest, which cools nothing and re-dials the same dead cluster.
func TestClassifyNVIDIMessage(t *testing.T) {
	cases := []struct {
		name      string
		errType   string
		code      int
		status    string
		message   string
		wantKind  Kind
		wantScope Scope
	}{
		{
			name:      "nvidia degraded 400 message",
			code:      http.StatusBadRequest,
			message:   "DEGRADED function cannot be invoked",
			wantKind:  KindModelUnavailable,
			wantScope: ScopeModel,
		},
		{
			name:      "degraded lower case, no status",
			message:   "the model cluster is degraded",
			wantKind:  KindModelUnavailable,
			wantScope: ScopeModel,
		},
		{
			name:      "degraded status field only, no message",
			code:      http.StatusServiceUnavailable,
			message:   "DEGRADED",
			wantKind:  KindModelUnavailable,
			wantScope: ScopeModel,
		},
		{
			name:      "status alone without degraded",
			code:      http.StatusBadRequest,
			message:   "invalid request",
			wantKind:  KindBadRequest,
			wantScope: ScopeRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := ClassifyDetail(tc.errType, tc.code, tc.status, tc.message)
			if c.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", c.Kind, tc.wantKind)
			}
			if c.EffectiveScope() != tc.wantScope {
				t.Errorf("Scope = %q, want %q", c.EffectiveScope(), tc.wantScope)
			}
		})
	}
}
