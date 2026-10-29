package translator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// TestPrefetch_RefusalPrefixDoesNotStarveTheBudget pins the wave loop. The
// sequential walk charged the budget only on success, so a run of broken URLs
// cost nothing and the walk continued to the images behind them. A fixed
// window over the first N candidates broke that: with N below the length of the
// broken prefix, nothing behind it was fetched and the body inlined zero images
// from an untouched budget.
func TestPrefetch_RefusalPrefixDoesNotStarveTheBudget(t *testing.T) {
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		if strings.HasPrefix(r.URL.Path, "/broken") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer srv.Close()

	mk := func(name string) string {
		return `{"type":"image_url","image_url":{"url":"` + srv.URL + `/` + name + `"}}`
	}
	// A broken prefix longer than any single wave the budget can ask for, then
	// real images. The budget is four and is untouched by the refusals.
	parts := make([]string, 0, 20)
	for i := range 20 {
		parts = append(parts, mk("broken"+strconv.Itoa(i)))
	}
	for _, name := range []string{"good1.png", "good2.png", "good3.png", "good4.png"} {
		parts = append(parts, mk(name))
	}
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[` + strings.Join(parts, ",") + `]}]}`)

	out := string(PrefetchInlineImages(context.Background(), body, PrefetchOptions{AllowLoopback: true}))
	if got := strings.Count(out, "data:image/png;base64,"); got != 4 {
		t.Fatalf("inlined %d images behind a broken prefix, want 4 (budget untouched):\n%s", got, out)
	}
	if n := downloads.Load(); n > 20+4 {
		t.Fatalf("%d downloads for 20 refusals and 4 images: the refusal ceiling is not bounding the pass", n)
	}
}

// TestPrefetch_HealthyBodyCostsOneWave pins the cost the parallel pass was
// introduced for: a body whose images all resolve must fetch each URL once and
// stop, not keep starting waves. Four images at the default budget of four is
// exactly one wave.
func TestPrefetch_HealthyBodyCostsOneWave(t *testing.T) {
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer srv.Close()

	parts := make([]string, 0, 4)
	for i := range 4 {
		parts = append(parts, `{"type":"image_url","image_url":{"url":"`+srv.URL+`/good`+strconv.Itoa(i)+`.png"}}`)
	}
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[` + strings.Join(parts, ",") + `]}]}`)

	out := string(PrefetchInlineImages(context.Background(), body, PrefetchOptions{AllowLoopback: true}))
	if got := strings.Count(out, "data:image/png;base64,"); got != 4 {
		t.Fatalf("inlined %d images, want 4:\n%s", got, out)
	}
	if n := downloads.Load(); n != 4 {
		t.Fatalf("%d downloads for 4 images: a healthy body must fetch each URL exactly once", n)
	}
}
