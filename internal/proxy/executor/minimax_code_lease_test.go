package executor

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/executor/mcodelease"
)

// newMinimaxLeaseRequest builds the request a lease connection arrives with:
// the connection's data carries authType mcode-lease, and the stored APIKey is
// the placeholder the dashboard writes for a connection that owns no token.
func newMinimaxLeaseRequest(t *testing.T) *Request {
	t.Helper()
	return &Request{
		Ctx:       t.Context(),
		Client:    &http.Client{Timeout: 10 * time.Second},
		Config:    &providers.ProviderConfig{BaseURL: "https://api.minimax.io", Format: "claude"},
		Provider:  "minimax-code",
		ModelName: "MiniMax-M2",
		APIKey:    "sk-xxx",
		Body:      []byte(`{"model":"MiniMax-M2","messages":[{"role":"user","content":"hi"}]}`),
		ConnData:  map[string]any{"authType": "mcode-lease"},
	}
}

// The header the broker's token ends up in. A leased connection's whole point
// is that the bearer is the desktop app's, so this is the seam the wiring is
// judged on.
func TestMinimaxCodeHeadersUsesLeasedBearer(t *testing.T) {
	req := newMinimaxLeaseRequest(t)
	req.APIKey = "leased-token-xyz"
	h := minimaxCodeHeaders(req)
	if got, want := h["Authorization"], "Bearer leased-token-xyz"; got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
	// The placeholder the gateway requires beside the real bearer must
	// survive a lease, exactly as it does for an OAuth connection.
	if got := h["x-api-key"]; got != "sk-xxx" {
		t.Errorf("x-api-key = %q, want the placeholder", got)
	}
}

// A non-lease connection must keep its stored key: lease mode is opt-in per
// connection, and an ordinary minimax-code key must not be replaced.
func TestIsLeaseConnectionGatesBorrow(t *testing.T) {
	for _, tt := range []struct {
		name string
		psd  map[string]any
		want bool
	}{
		{"lease", map[string]any{"authType": "mcode-lease"}, true},
		{"oauth", map[string]any{"authType": "oauth"}, false},
		{"absent", map[string]any{"fingerprintId": "f"}, false},
		{"nil", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := mcodelease.IsLeaseConnection(tt.psd); got != tt.want {
				t.Errorf("IsLeaseConnection = %v, want %v", got, tt.want)
			}
		})
	}
}

// ForwardMinimaxCode must not reach the network for a lease connection whose
// broker is absent: the lease failure is the answer, and no upstream request
// is issued with a placeholder bearer.
func TestForwardMinimaxCodeBrokerUnavailableBeforeUpstream(t *testing.T) {
	var upstreamHit bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	req := newMinimaxLeaseRequest(t)
	req.Config.BaseURL = upstream.URL
	// A data dir with no capability file: no desktop install is running.
	req.ConnData = map[string]any{"authType": "mcode-lease", "mcodeDataDir": t.TempDir()}

	rec := httptest.NewRecorder()
	err := ForwardMinimaxCode(rec, req)
	if err == nil {
		t.Fatal("a missing broker was not reported")
	}
	var le *mcodelease.AuthLeaseError
	if !errors.As(err, &le) || le.Code != mcodelease.CodeBrokerUnavailable {
		t.Fatalf("err = %v, want BROKER_UNAVAILABLE", err)
	}
	if upstreamHit {
		t.Error("upstream was called with a placeholder bearer")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want nothing written", rec.Body.String())
	}
}
