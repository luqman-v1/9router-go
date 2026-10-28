package translator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestPrefetch_FetchesImagesConcurrently pins the parallel fetch: a turn that
// carries several remote images must download them at the same time, not one
// after another. The server records the widest overlap it ever saw; a
// sequential walk can only ever produce one.
func TestPrefetch_FetchesImagesConcurrently(t *testing.T) {
	var inFlight, maxInFlight atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := inFlight.Add(1)
		for {
			old := maxInFlight.Load()
			if cur <= old || maxInFlight.CompareAndSwap(old, cur) {
				break
			}
		}
		defer inFlight.Add(-1)
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer srv.Close()

	body := []byte(`{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"` + srv.URL + `/a.png"}},` +
		`{"type":"image_url","image_url":{"url":"` + srv.URL + `/b.png"}},` +
		`{"type":"image_url","image_url":{"url":"` + srv.URL + `/c.png"}},` +
		`{"type":"image_url","image_url":{"url":"` + srv.URL + `/d.png"}}]}]}`)

	out := string(PrefetchInlineImages(context.Background(), body, PrefetchOptions{AllowLoopback: true}))
	if got := maxInFlight.Load(); got < 2 {
		t.Fatalf("images were fetched sequentially (max in flight = %d)", got)
	}
	if strings.Count(out, "data:image/png;base64,") != 4 {
		t.Fatalf("expected all four images inlined, got:\n%s", out)
	}
}

// TestPrefetch_BudgetAppliesInDocumentOrder pins that the budget lands on the
// first inlinable images in document order — the same contract the sequential
// walk had. Later URLs keep their remote form once the budget is spent.
func TestPrefetch_BudgetAppliesInDocumentOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer srv.Close()

	mk := func(name string) string {
		return `{"type":"image_url","image_url":{"url":"` + srv.URL + `/` + name + `"}}`
	}
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[` +
		mk("first") + `,` + mk("second") + `,` + mk("third") + `]}]}`)

	out := string(PrefetchInlineImages(context.Background(), body, PrefetchOptions{AllowLoopback: true, MaxImages: 1}))
	inlined := strings.Count(out, "data:image/png;base64,")
	if inlined != 1 {
		t.Fatalf("expected exactly one inlined image, got %d:\n%s", inlined, out)
	}
	remote := strings.Count(out, srv.URL+"/")
	if remote != 2 {
		t.Fatalf("expected the two unbudgeted URLs kept remote, got %d remote:\n%s", remote, out)
	}
	if strings.Contains(out, "/first") {
		t.Fatalf("the first URL was not the one inlined:\n%s", out)
	}
}

// TestPrefetch_SharedClientAcrossPasses pins that two passes share one client:
// the transport's idle pool is what lets consecutive turns reuse a warm TLS
// connection to the image host. The second pass must not rebuild it.
func TestPrefetch_SharedClientAcrossPasses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer srv.Close()

	body := []byte(`{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"` + srv.URL + `/a.png"}}]}]}`)

	first := PrefetchInlineImages(context.Background(), body, PrefetchOptions{AllowLoopback: true})
	second := PrefetchInlineImages(context.Background(), body, PrefetchOptions{AllowLoopback: true})

	prefetchClientMu.Lock()
	shared := defaultPrefetchClient != nil || loopbackPrefetchClient != nil
	prefetchClientMu.Unlock()
	if !shared {
		t.Fatal("no cached client after two passes; each pass still builds its own transport")
	}
	for _, out := range []string{string(first), string(second)} {
		if !strings.Contains(out, "data:image/png;base64,") {
			t.Fatalf("pass lost the image:\n%s", out)
		}
	}
}
