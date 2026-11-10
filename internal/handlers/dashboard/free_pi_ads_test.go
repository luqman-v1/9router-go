package dashboard

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// fakeFreePiAds stands in for the ad network: it serves a creative, records the
// pixel fetch, and counts impression posts. Every count here backs an assertion
// that the gateway did NOT fabricate a view.
type fakeFreePiAds struct {
	srv *httptest.Server

	pixelHits      int32
	impressionHits int32
	nextStatus     int
	nextBody       string
	impStatus      int
}

func newFakeFreePiAds(t *testing.T) *fakeFreePiAds {
	t.Helper()
	f := &fakeFreePiAds{nextStatus: http.StatusOK, impStatus: http.StatusOK}

	// The pixel host is validated against providers.FreePiImpressionHosts, so the
	// fake advertises a URL on an allowlisted host and the test rewrites it to
	// the local server through the transport below.
	pixel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.pixelHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(pixel.Close)

	f.nextBody = `{
		"ad_id": "ad_1",
		"click_token": "ct_1",
		"imp_url": "` + pixel.URL + `/imp.gif",
		"click_url": "https://track.example/click",
		"source": "gravity",
		"creative": {"title": "Ship faster", "body": "Observability for teams", "cta": "Start free", "image_url": "https://cdn.example/c.png"}
	}`

	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ads/next":
			w.WriteHeader(f.nextStatus)
			if f.nextStatus == http.StatusNoContent {
				return
			}
			_, _ = w.Write([]byte(f.nextBody))
		case "/ads/impression":
			atomic.AddInt32(&f.impressionHits, 1)
			w.WriteHeader(f.impStatus)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)

	freePiResetAdsForTest()
	return f
}

func (f *fakeFreePiAds) counts() (pixel, impression int32) {
	return atomic.LoadInt32(&f.pixelHits), atomic.LoadInt32(&f.impressionHits)
}

// adsTestEnv is a dashboard handler wired to a real in-memory database holding
// one free-pi connection pointing at the fake ad network.
type adsTestEnv struct {
	handler *DashboardHandler
	connID  string
}

func newAdsTestEnv(t *testing.T, ads *fakeFreePiAds, mutate func(*atomic.Bool)) *adsTestEnv {
	t.Helper()

	conn, err := db.OpenDatabase(t.TempDir() + "/ads.sqlite")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.EnsureCoreSchema(conn); err != nil {
		t.Fatalf("schema: %v", err)
	}
	repo := db.NewRepo(conn)
	t.Cleanup(func() { conn.Close() })

	// The client is swapped for one that dials the loopback fakes, because the
	// shared transport refuses private addresses (SSRF protection).
	prev := freePiAdClient
	freePiAdClient = ads.srv.Client()
	t.Cleanup(func() { freePiAdClient = prev })

	data := `{"apiKey":"free-pi-jwt","baseUrl":"` + ads.srv.URL + `/v1/chat/completions"}`
	priority := 1
	if err := repo.CreateProviderConnectionFull("conn-ads", providers.FreePiProviderID, "apikey",
		"Free Pi ads", &priority, data); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	return &adsTestEnv{handler: NewDashboardHandler(repo, nil), connID: "conn-ads"}
}

// freePiAdResponse mirrors the GET /api/free-pi/ad envelope.
type freePiAdResponse struct {
	Ad *freePiAd `json:"ad"`
}

// decodeJSONInto unmarshals a response body, failing the test on malformed JSON.
func decodeJSONInto(t *testing.T, raw []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode %q: %v", string(raw), err)
	}
}

// jsonBody encodes a request payload for httptest.
func jsonBody(payload map[string]any) io.Reader {
	raw, _ := json.Marshal(payload)
	return bytes.NewReader(raw)
}

func (e *adsTestEnv) getAd(t *testing.T) (*httptest.ResponseRecorder, freePiAdResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/free-pi/ad?connectionId="+e.connID, nil)
	rec := httptest.NewRecorder()
	e.handler.HandleGetFreePiAd(rec, req)

	var body freePiAdResponse
	if rec.Code == http.StatusOK {
		decodeJSONInto(t, rec.Body.Bytes(), &body)
	}
	return rec, body
}

func (e *adsTestEnv) reportImpression(t *testing.T, connID, adID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/free-pi/ad/impression",
		jsonBody(map[string]any{"connectionId": connID, "adId": adID}))
	rec := httptest.NewRecorder()
	e.handler.HandleFreePiImpression(rec, req)
	return rec
}

// TestFreePiAd_FetchServesCreativeWithoutReporting is the honesty invariant: the
// ad network must see ZERO impressions at fetch time. An impression fired on a
// timer or on serve is the misreport this design exists to avoid.
func TestFreePiAd_FetchServesCreativeWithoutReporting(t *testing.T) {
	ads := newFakeFreePiAds(t)
	env := newAdsTestEnv(t, ads, nil)

	rec, body := env.getAd(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if body.Ad == nil {
		t.Fatal("no ad in the response")
	}
	if body.Ad.AdID != "ad_1" {
		t.Errorf("adId = %q, want ad_1", body.Ad.AdID)
	}
	if body.Ad.Creative.Title != "Ship faster" {
		t.Errorf("creative title = %q, want the served creative", body.Ad.Creative.Title)
	}

	pixel, impression := ads.counts()
	if impression != 0 {
		t.Errorf("impressions at fetch time = %d, want 0 — serving an ad is not seeing one", impression)
	}
	if pixel != 0 {
		t.Errorf("pixel hits at fetch time = %d, want 0", pixel)
	}
}

// TestFreePiAd_ImpressionRequiresAServedCreative: a client cannot post an
// impression for an ad this gateway never handed out.
func TestFreePiAd_ImpressionRequiresAServedCreative(t *testing.T) {
	ads := newFakeFreePiAds(t)
	env := newAdsTestEnv(t, ads, nil)

	rec := env.reportImpression(t, env.connID, "ad_never_served")
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 for an unserved ad", rec.Code)
	}
	if _, impression := ads.counts(); impression != 0 {
		t.Errorf("impressions = %d, want 0", impression)
	}
}

// TestFreePiAd_ImpressionAfterRenderIsReportedOnce covers the happy path and the
// single-report rule: one serving, one impression.
func TestFreePiAd_ImpressionAfterRenderIsReportedOnce(t *testing.T) {
	ads := newFakeFreePiAds(t)
	env := newAdsTestEnv(t, ads, nil)

	// The pixel the fake serves is on loopback, which the real guard refuses by
	// design. Substituting the validator here keeps the production rule intact
	// (TestFreePiAdURLAllowed pins it) while exercising the report path.
	prevValidator := freePiAdURLValidator
	freePiAdURLValidator = func(string) bool { return true }
	t.Cleanup(func() { freePiAdURLValidator = prevValidator })

	if rec, body := env.getAd(t); rec.Code != http.StatusOK || body.Ad == nil {
		t.Fatalf("fetch failed: %d %s", rec.Code, rec.Body.String())
	}

	rec := env.reportImpression(t, env.connID, "ad_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	pixel, impression := ads.counts()
	if impression != 1 {
		t.Errorf("impressions = %d, want 1", impression)
	}
	if pixel != 1 {
		t.Errorf("pixel hits = %d, want 1 (the network's own measurement)", pixel)
	}

	// The same view reported twice must be refused: the serving was consumed.
	again := env.reportImpression(t, env.connID, "ad_1")
	if again.Code != http.StatusConflict {
		t.Errorf("second report status = %d, want 409", again.Code)
	}
	if _, n := ads.counts(); n != 1 {
		t.Errorf("impressions after a second report = %d, want 1", n)
	}
}

// TestFreePiAd_NoAdIsNotAnError: a 204 from the ad network means nothing to
// show, which must read as "no banner", not as a provider failure.
func TestFreePiAd_NoAdIsNotAnError(t *testing.T) {
	ads := newFakeFreePiAds(t)
	ads.nextStatus = http.StatusNoContent
	env := newAdsTestEnv(t, ads, nil)

	rec, body := env.getAd(t)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if body.Ad != nil {
		t.Errorf("ad = %+v, want nil", body.Ad)
	}
	if _, impression := ads.counts(); impression != 0 {
		t.Errorf("impressions = %d, want 0", impression)
	}
}

// TestFreePiAd_UnknownConnectionIs404 keeps the endpoint from being an open
// proxy: it only ever fetches ads for a stored free-pi connection.
func TestFreePiAd_UnknownConnectionIs404(t *testing.T) {
	ads := newFakeFreePiAds(t)
	env := newAdsTestEnv(t, ads, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/free-pi/ad?connectionId=nope", nil)
	rec := httptest.NewRecorder()
	env.handler.HandleGetFreePiAd(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if _, impression := ads.counts(); impression != 0 {
		t.Errorf("impressions = %d, want 0", impression)
	}
}

// --- allowlist ------------------------------------------------------------

// TestFreePiAdURLAllowed pins the SSRF guard. imp_url is upstream-supplied and we
// fetch it server-side, so anything off the allowlisted hosts must be refused —
// including an allowlisted name worn as a suffix by a lookalike domain.
func TestFreePiAdURLAllowed(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "gravity ai", url: "https://track.trygravity.ai/imp/1.gif", want: true},
		{name: "gravity com", url: "https://trygravity.com/imp/1.gif", want: true},
		{name: "bare host", url: "https://trygravity.ai/imp/1.gif", want: true},

		{name: "lookalike suffix", url: "https://trygravity.ai.evil.test/imp.gif", want: false},
		{name: "not a subdomain", url: "https://eviltrygravity.ai/imp.gif", want: false},
		{name: "unrelated host", url: "https://evil.test/imp.gif", want: false},
		{name: "plain http", url: "http://track.trygravity.ai/imp.gif", want: false},
		{name: "empty", url: "", want: false},
		{name: "not a url", url: "://bad", want: false},

		// Localhost is the SSRF target that matters: without the scheme+host
		// check this would let upstream point us at the gateway's own loopback.
		{name: "loopback", url: "https://127.0.0.1/imp.gif", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := freePiAdURLAllowed(tt.url); got != tt.want {
				t.Errorf("freePiAdURLAllowed(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

// TestFreePiReportImpression_RefusesForeignPixel proves the guard is wired in,
// not merely present: a creative whose imp_url points off-allowlist must not be
// fetched, and its impression must not be claimed either.
func TestFreePiReportImpression_RefusesForeignPixel(t *testing.T) {
	ads := newFakeFreePiAds(t)
	newAdsTestEnv(t, ads, nil)

	ad := freePiAd{
		AdID:       "ad_x",
		ClickToken: "ct_x",
		ImpURL:     "https://evil.test/imp.gif",
	}
	err := freePiReportImpression(t.Context(), "free-pi-jwt", ads.srv.URL+"/v1/chat/completions", ad)
	if err == nil {
		t.Fatal("expected a refusal for a non-allowlisted pixel host")
	}
	if _, impression := ads.counts(); impression != 0 {
		t.Errorf("impressions = %d, want 0 — an unmeasurable view must not be claimed", impression)
	}
}

// --- pending table --------------------------------------------------------

func TestFreePiPendingAdsAreBounded(t *testing.T) {
	freePiResetAdsForTest()

	for i := 0; i < freePiAdEntries*2; i++ {
		freePiStorePendingAd(string(rune('a'+i%26))+string(rune('0'+i%10))+"-"+string(rune('A'+i%26)), freePiAd{AdID: "ad"})
	}

	freePiAdMu.Lock()
	size := len(freePiPending)
	freePiAdMu.Unlock()
	if size > freePiAdEntries {
		t.Errorf("pending table grew to %d entries, cap is %d", size, freePiAdEntries)
	}
}

var _ = sync.Mutex{}
