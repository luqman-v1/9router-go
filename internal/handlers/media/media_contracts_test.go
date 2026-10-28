package media

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

func TestMediaContracts_ClientCancellationAbortsUpstream(t *testing.T) {
	upstreamHit, upstreamCanceled := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		close(upstreamHit)
		<-r.Context().Done()
		close(upstreamCanceled)
	}))
	defer upstream.Close()

	origCfg := providers.KnownProviders["openai"]
	testCfg := origCfg
	testCfg.BaseURL = upstream.URL
	providers.KnownProviders["openai"] = testCfg
	defer func() { providers.KnownProviders["openai"] = origCfg }()

	d, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(d)
	_ = repo.CreateProviderConnection("conn-stt", "openai", "apikey", "STT Test", "sk-test")
	handler := newTestMediaHandler(repo)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequestWithContext(ctx, "POST", "/v1/audio/transcriptions", strings.NewReader(`{"model":"whisper-1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	go func() {
		<-upstreamHit
		cancel()
	}()

	// The handler must run concurrently with the bound. An earlier draft called
	// it synchronously and then waited, so the 2s arm could only be reached
	// after the handler returned: severing the client context from the upstream
	// request made the handler stall until the transport's 120s header timeout
	// and the test still passed, because by then the upstream socket closed for
	// an unrelated reason. Only a concurrent wait can tell the two apart.
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.HandleAudioTranscriptions(rec, req)
	}()

	select {
	case <-upstreamCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("client cancellation did not abort upstream socket within 2s")
	}
	<-done
}

// TestMediaContracts_WebFetch_CanceledContext pins that a cancelled client
// never reaches the upstream. The earlier draft posted to an unconfigured
// model, so the handler answered 400 "could not resolve model" whether or not
// the context was cancelled — the assertion held for a reason unrelated to
// cancellation and could not fail if context propagation were severed. Here
// model resolution succeeds and the upstream is a local server, so the only
// thing that can keep the request from arriving is the client's context.
func TestMediaContracts_WebFetch_CanceledContext(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":"# Title"}`))
	}))
	defer upstream.Close()

	jr := providers.KnownProviders["jina-reader"]
	origFetch, origBase := jr.FetchURL, jr.BaseURL
	jr.FetchURL, jr.BaseURL = upstream.URL, upstream.URL
	providers.KnownProviders["jina-reader"] = jr
	defer func() {
		jr.FetchURL, jr.BaseURL = origFetch, origBase
		providers.KnownProviders["jina-reader"] = jr
	}()

	d, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	seedFetchConn(t, d, "jina-reader", "conn-jina", "sk-test")
	handler := newTestMediaHandler(db.NewRepo(d))

	// A literal public IP keeps the SSRF check off the DNS resolver, so the test
	// needs no network and cannot be slowed by one.
	body := `{"model":"jina-reader/fetch","url":"http://93.184.216.34/page"}`

	// The un-cancelled control proves the fixture reaches the upstream at all;
	// without it a mis-seeded connection would make the cancelled case pass
	// for the same reason the old draft did.
	live := httptest.NewRecorder()
	handler.HandleWebFetch(live, httptest.NewRequest("POST", "/v1/web/fetch", strings.NewReader(body)))
	if live.Code != http.StatusOK || atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("control request did not reach upstream: status %d, hits %d", live.Code, hits)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, "POST", "/v1/web/fetch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleWebFetch(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-OK response on canceled context, got %d", rec.Code)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("a cancelled request reached the upstream %d extra time(s); the client context is not on the request",
			got-1)
	}
}
