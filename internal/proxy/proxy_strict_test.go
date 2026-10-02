package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/constants"
)

// newPoolTransport builds the transport shape the chat handler caches per
// proxy URL: one pooled client that dials the assigned proxy.
func newPoolTransport(proxyURL string) *http.Transport {
	tr := constants.DefaultHTTPTransportConfig.NewTransport()
	u, err := url.Parse(proxyURL)
	if err != nil {
		panic(err)
	}
	tr.Proxy = http.ProxyURL(u)
	return tr
}

func poolClient(proxyURL string) *http.Client {
	return &http.Client{Transport: newPoolTransport(proxyURL)}
}

// newDeadProxy returns an HTTP proxy that refuses everything — the shape
// upstream's strictProxy branch exists for: a pool that is assigned but dead.
// It answers 403 as text/plain, the refusal isProxyFailure recognises.
func newDeadProxy(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "proxy refuses to tunnel", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// okUpstream answers 200 and counts how often it was reached directly. A
// request that answered here came out of the real IP, which is what the strict
// tests must never observe.
func okUpstream(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestDoRequestStrictProxyNeverReplaysDirect is the #4333 leak, proved
// positively: the direct upstream must see nothing at all. A request marked
// strict may not leave over the operator's IP once the assigned proxy fails.
// The proxy's own 403 is still what DoRequest reports — what matters is that
// nothing was replayed underneath it.
func TestDoRequestStrictProxyNeverReplaysDirect(t *testing.T) {
	cases := []struct {
		name   string
		ctx    func(context.Context) context.Context
		client func(proxyURL string) *http.Client
	}{
		{
			name: "strict connection refuses the direct replay",
			ctx:  func(ctx context.Context) context.Context { return WithStrictProxy(ctx, true) },
			client: func(proxyURL string) *http.Client {
				return poolClient(proxyURL)
			},
		},
		{
			name: "strict pool client refuses without the context marker",
			ctx:  func(ctx context.Context) context.Context { return ctx },
			client: func(proxyURL string) *http.Client {
				return ForbidDirectReplay(poolClient(proxyURL))
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			upstream, directHits := okUpstream(t)
			proxySrv, proxyHits := newDeadProxy(t)

			if _, err := DoRequest(tt.ctx(context.Background()), tt.client(proxySrv.URL),
				http.MethodPost, upstream.URL, nil, []byte(`{"model":"x"}`)); err == nil {
				t.Fatal("the dead proxy's refusal must surface instead of a direct 200")
			}
			if directHits.Load() != 0 {
				t.Errorf("the direct upstream answered %d times: strict traffic escaped over the real IP", directHits.Load())
			}
			if proxyHits.Load() == 0 {
				t.Error("the assigned proxy was never dialed, so nothing was refused")
			}
		})
	}
}

// TestDoRequestStrictProxyRefusesWhenProxyIsUnreachable is the other refusal
// shape: the proxy is assigned but does not answer at all, so the failure
// arrives as a dial error rather than an HTTP status.
func TestDoRequestStrictProxyRefusesWhenProxyIsUnreachable(t *testing.T) {
	upstream, directHits := okUpstream(t)
	deadPort := closedProxyPort(t)

	client := ForbidDirectReplay(poolClient("http://"+deadPort))
	if _, err := DoRequest(context.Background(), client, http.MethodPost, upstream.URL, nil, []byte(`{}`)); err == nil {
		t.Fatal("an unreachable strict proxy must fail the request")
	}
	if directHits.Load() != 0 {
		t.Errorf("the direct upstream answered %d times: strict traffic escaped over the real IP", directHits.Load())
	}
}

// closedProxyPort returns an address nothing listens on.
func closedProxyPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release the reserved port: %v", err)
	}
	return addr
}

// TestDoRequestStrictProxyAllowsDirectWhenNothingConfigured covers the other
// half of upstream's gate. Executors such as Qoder pass strictProxy with
// nothing configured, meaning "never replay this request directly" — not "a
// proxy must exist". Refusing those callers would take a working provider
// down, so the flag alone may not block a direct request.
func TestDoRequestStrictProxyAllowsDirectWhenNothingConfigured(t *testing.T) {
	upstream, directHits := okUpstream(t)

	ctx := WithStrictProxy(context.Background(), false)
	if _, err := DoRequest(ctx, &http.Client{}, http.MethodPost, upstream.URL, nil, []byte(`{}`)); err != nil {
		t.Fatalf("strict with no proxy configured must keep working direct: %v", err)
	}
	if directHits.Load() != 1 {
		t.Errorf("direct hits = %d, want the request to reach the upstream", directHits.Load())
	}
}

// TestDoRequestAssignedPoolRefusesDirectReplay pins the egress decision that
// outlived strictProxy: a proxy pool the operator assigned never degrades to
// direct, strict flag or not (#105). Replaying would answer the request from
// the host's real IP while the dashboard still shows the connection as proxied.
// An ambient proxy is the case that keeps its escape hatch — see
// TestDoRequest_AmbientProxyStillFallsBackToDirect.
func TestDoRequestAssignedPoolRefusesDirectReplay(t *testing.T) {
	upstream, directHits := okUpstream(t)
	proxySrv, _ := newDeadProxy(t)

	_, err := DoRequest(context.Background(), poolClient(proxySrv.URL),
		http.MethodPost, upstream.URL, nil, []byte(`{}`))
	if err == nil {
		t.Fatal("an assigned pool that refuses the tunnel must fail, not answer direct")
	}
	if !errors.Is(err, ErrStrictProxyRequired) {
		t.Errorf("error = %v, want it to name the egress refusal", err)
	}
	if directHits.Load() != 0 {
		t.Errorf("direct hits = %d, want 0: assigned-pool traffic escaped over the real IP", directHits.Load())
	}
}

// TestForbidDirectReplayLeavesLaxClientsAlone guards the pooled-client marker
// against over-reach: only the wrapper opts in, so a plain client can still
// degrade to direct.
func TestForbidDirectReplayLeavesLaxClientsAlone(t *testing.T) {
	lax := poolClient("http://127.0.0.1:1")
	if forbidsDirectReplay(lax) {
		t.Error("an unmarked client must not forbid the direct replay")
	}
	if forbidsDirectReplay(nil) {
		t.Error("a nil client must not forbid the direct replay")
	}
	if !forbidsDirectReplay(ForbidDirectReplay(lax)) {
		t.Error("ForbidDirectReplay must mark the returned client")
	}
	if _, ok := lax.Transport.(*http.Transport); !ok {
		t.Errorf("the lax client transport type = %T, want the pooled *http.Transport", lax.Transport)
	}
}