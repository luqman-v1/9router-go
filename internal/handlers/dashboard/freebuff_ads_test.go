package dashboard

import (
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
)

type fakeFreebuffAdsServer struct {
	srv             *httptest.Server
	auctionCalls    int64
	impressionCalls int64
	lastImpURL      string
	lastMode        string
	lastSurface     string
	fill            bool
}

func newFakeFreebuffAds(t *testing.T) *fakeFreebuffAdsServer {
	t.Helper()
	fake := &fakeFreebuffAdsServer{fill: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ads", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&fake.auctionCalls, 1)
		if r.Header.Get("Authorization") != "Bearer fb-secret-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !fake.fill {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, map[string]any{
			"ads": []map[string]any{
				{
					"title":       "Build with Buffy",
					"adText":      "Free AI coding for everyone",
					"cta":         "Learn More",
					"clickUrl":    "https://freebuff.com/start",
					"impUrl":      "https://codebuff.com/imp/test-token-123",
					"favicon":     "https://freebuff.com/favicon.ico",
					"sponsorName": "Codebuff Inc",
					"brandColor":  "#10b981",
				},
			},
			"provider": "first_party",
		})
	})

	mux.HandleFunc("/api/v1/ads/impression", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&fake.impressionCalls, 1)
		var body struct {
			ImpURL  string `json:"impUrl"`
			Mode    string `json:"mode"`
			Surface string `json:"surface"`
		}
		_ = json.UnmarshalRead(r.Body, &body)
		fake.lastImpURL = body.ImpURL
		fake.lastMode = body.Mode
		fake.lastSurface = body.Surface
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"recorded":true}`))
	})

	fake.srv = httptest.NewServer(mux)
	t.Cleanup(fake.srv.Close)
	return fake
}

func newFreebuffAdsTestEnv(t *testing.T, ads *fakeFreebuffAdsServer) (*DashboardHandler, string) {
	t.Helper()
	conn, err := db.OpenDatabase(t.TempDir() + "/fb_ads.sqlite")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.EnsureCoreSchema(conn); err != nil {
		t.Fatalf("schema: %v", err)
	}
	repo := db.NewRepo(conn)
	t.Cleanup(func() { _ = conn.Close() })

	prev := freebuffAdClient
	freebuffAdClient = ads.srv.Client()
	t.Cleanup(func() { freebuffAdClient = prev })

	connID := "fb-conn-ads-1"
	connData := fmt.Sprintf(`{"authToken":"fb-secret-token","baseUrl":"%s"}`, ads.srv.URL)
	priority := 1
	if err := repo.CreateProviderConnectionFull(connID, "freebuff", "oauth",
		"Freebuff Ads Test", &priority, connData); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	handler := NewDashboardHandler(repo, nil)
	return handler, connID
}

func TestFreebuffAd_FetchAndImpressionCycle(t *testing.T) {
	freebuffResetAdsForTest()
	ads := newFakeFreebuffAds(t)
	handler, connID := newFreebuffAdsTestEnv(t, ads)

	// 1. Fetch ad
	req := httptest.NewRequest(http.MethodGet, "/api/freebuff/ad?connectionId="+connID, nil)
	rec := httptest.NewRecorder()
	handler.HandleGetFreebuffAd(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var fetchRes struct {
		Ad *freebuffAd `json:"ad"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &fetchRes); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if fetchRes.Ad == nil || fetchRes.Ad.AdID == "" {
		t.Fatalf("expected non-empty ad and adId")
	}
	if fetchRes.Ad.Creative.Title != "Build with Buffy" {
		t.Errorf("expected Title 'Build with Buffy', got %q", fetchRes.Ad.Creative.Title)
	}

	// 2. Report impression
	body := fmt.Sprintf(`{"connectionId":"%s","adId":"%s"}`, connID, fetchRes.Ad.AdID)
	reqImp := httptest.NewRequest(http.MethodPost, "/api/freebuff/ad/impression", strings.NewReader(body))
	recImp := httptest.NewRecorder()
	handler.HandleFreebuffImpression(recImp, reqImp)

	if recImp.Code != http.StatusOK {
		t.Fatalf("impression status = %d, want 200: %s", recImp.Code, recImp.Body.String())
	}
	if ads.impressionCalls != 1 {
		t.Errorf("expected 1 impression call, got %d", ads.impressionCalls)
	}
	if ads.lastImpURL != "https://codebuff.com/imp/test-token-123" {
		t.Errorf("unexpected impUrl reported: %q", ads.lastImpURL)
	}
	if ads.lastSurface != "cli_chat" || ads.lastMode != "LITE" {
		t.Errorf("unexpected surface/mode: %s/%s", ads.lastSurface, ads.lastMode)
	}

	// 3. Second impression on same adId is refused (one serving, one impression)
	recImp2 := httptest.NewRecorder()
	reqImp2 := httptest.NewRequest(http.MethodPost, "/api/freebuff/ad/impression", strings.NewReader(body))
	handler.HandleFreebuffImpression(recImp2, reqImp2)
	if recImp2.Code != http.StatusConflict {
		t.Fatalf("second impression status = %d, want 409", recImp2.Code)
	}
}

func TestFreebuffAd_NoFillReturnsNull(t *testing.T) {
	freebuffResetAdsForTest()
	ads := newFakeFreebuffAds(t)
	ads.fill = false
	handler, connID := newFreebuffAdsTestEnv(t, ads)

	req := httptest.NewRequest(http.MethodGet, "/api/freebuff/ad?connectionId="+connID, nil)
	rec := httptest.NewRecorder()
	handler.HandleGetFreebuffAd(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var res struct {
		Ad *freebuffAd `json:"ad"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Ad != nil {
		t.Errorf("expected nil ad on 204 no-fill, got %+v", res.Ad)
	}
}

func TestFreebuffAd_UnknownConnectionIs404(t *testing.T) {
	ads := newFakeFreebuffAds(t)
	handler, _ := newFreebuffAdsTestEnv(t, ads)

	req := httptest.NewRequest(http.MethodGet, "/api/freebuff/ad?connectionId=non-existent", nil)
	rec := httptest.NewRecorder()
	handler.HandleGetFreebuffAd(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestFreebuffAd_EvictsOldestWhenFull(t *testing.T) {
	freebuffResetAdsForTest()
	for i := range freebuffAdEntries * 2 {
		connKey := fmt.Sprintf("conn-%d", i)
		freebuffStorePendingAd(connKey, freebuffAd{AdID: fmt.Sprintf("ad-%d", i), ImpURL: "imp"})
	}

	freebuffAdMu.Lock()
	count := len(freebuffPending)
	freebuffAdMu.Unlock()

	if count > freebuffAdEntries {
		t.Errorf("count %d exceeded capacity %d", count, freebuffAdEntries)
	}
}
