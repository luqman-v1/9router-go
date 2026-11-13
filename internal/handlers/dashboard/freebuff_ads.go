package dashboard

import (
	"bytes"
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/proxy"
)

const (
	freebuffAdTTL      = 15 * time.Minute
	freebuffAdEntries  = 64
	freebuffAdsTimeout = 8 * time.Second
)

var freebuffAdClient = &http.Client{
	Transport: proxy.NewFallbackTransport(http.DefaultTransport),
	Timeout:   freebuffAdsTimeout,
}

// freebuffCreative is the renderable part of a Freebuff ad.
type freebuffCreative struct {
	Title       string `json:"title,omitempty"`
	Body        string `json:"body,omitempty"`
	CTA         string `json:"cta,omitempty"`
	ImageURL    string `json:"imageUrl,omitempty"`
	SponsorName string `json:"sponsorName,omitempty"`
	BrandColor  string `json:"brandColor,omitempty"`
}

// freebuffAd is one ad served by POST /api/v1/ads.
type freebuffAd struct {
	AdID      string           `json:"adId"`
	ClickURL  string           `json:"clickUrl,omitempty"`
	Creative  freebuffCreative `json:"creative"`
	ImpURL    string           `json:"-"`
	FetchedAt time.Time        `json:"-"`
}

type freebuffPendingAd struct {
	ad        freebuffAd
	expiresAt time.Time
}

var (
	freebuffAdMu    sync.Mutex
	freebuffPending = make(map[string]freebuffPendingAd, freebuffAdEntries)
)

func freebuffOriginFromBase(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	for _, suffix := range []string{"/v1/chat/completions", "/chat/completions", "/v1"} {
		if trimmed := strings.TrimSuffix(base, suffix); trimmed != base {
			return trimmed
		}
	}
	if base == "" {
		return "https://www.codebuff.com"
	}
	return base
}

func freebuffAdIDForImpURL(impURL string) string {
	sum := sha256.Sum256([]byte(impURL))
	return fmt.Sprintf("fb-ad-%x", sum[:8])
}

// freebuffFetchAd requests a display ad from Freebuff upstream.
func freebuffFetchAd(ctx context.Context, token, baseURL string) (*freebuffAd, error) {
	endpoint := freebuffOriginFromBase(baseURL) + "/api/v1/ads"

	bodyPayload := map[string]any{
		"surface":     "cli_chat",
		"placementId": "CLI-Statusline",
	}
	rawBody, err := json.Marshal(bodyPayload)
	if err != nil {
		return nil, fmt.Errorf("freebuff ad: marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(rawBody))
	if err != nil {
		return nil, fmt.Errorf("freebuff ad: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "Freebuff-CLI/0.2.36")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := freebuffAdClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("freebuff ad: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("freebuff ad: HTTP %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("freebuff ad: read response: %w", err)
	}

	var wire struct {
		Ads []struct {
			Title       string `json:"title"`
			AdText      string `json:"adText"`
			CTA         string `json:"cta"`
			URL         string `json:"url"`
			ClickURL    string `json:"clickUrl"`
			ImpURL      string `json:"impUrl"`
			Favicon     string `json:"favicon"`
			SponsorName string `json:"sponsorName"`
			BrandColor  string `json:"brandColor"`
		} `json:"ads"`
		Provider string `json:"provider"`
	}

	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("freebuff ad: parse response: %w", err)
	}
	if len(wire.Ads) == 0 || wire.Ads[0].ImpURL == "" {
		return nil, nil
	}

	first := wire.Ads[0]
	clickURL := first.ClickURL
	if clickURL == "" {
		clickURL = first.URL
	}

	return &freebuffAd{
		AdID:     freebuffAdIDForImpURL(first.ImpURL),
		ClickURL: clickURL,
		Creative: freebuffCreative{
			Title:       first.Title,
			Body:        first.AdText,
			CTA:         first.CTA,
			ImageURL:    first.Favicon,
			SponsorName: first.SponsorName,
			BrandColor:  first.BrandColor,
		},
		ImpURL:    first.ImpURL,
		FetchedAt: time.Now(),
	}, nil
}

// freebuffReportImpression acknowledges one impression with upstream.
func freebuffReportImpression(ctx context.Context, token, baseURL string, ad freebuffAd) error {
	if ad.ImpURL == "" {
		return fmt.Errorf("freebuff ad: impression needs impUrl")
	}

	endpoint := freebuffOriginFromBase(baseURL) + "/api/v1/ads/impression"
	bodyPayload := map[string]any{
		"impUrl":  ad.ImpURL,
		"mode":    "LITE",
		"surface": "cli_chat",
	}
	rawBody, err := json.Marshal(bodyPayload)
	if err != nil {
		return fmt.Errorf("freebuff ad: marshal impression: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(rawBody))
	if err != nil {
		return fmt.Errorf("freebuff ad: build impression request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "Freebuff-CLI/0.2.36")
	req.Header.Set("Content-Type", "application/json")

	resp, err := freebuffAdClient.Do(req)
	if err != nil {
		return fmt.Errorf("freebuff ad: impression %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("freebuff ad: impression HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

func freebuffStorePendingAd(connID string, ad freebuffAd) {
	freebuffAdMu.Lock()
	defer freebuffAdMu.Unlock()

	now := time.Now()
	for k, v := range freebuffPending {
		if now.After(v.expiresAt) {
			delete(freebuffPending, k)
		}
	}
	if _, exists := freebuffPending[connID]; !exists && len(freebuffPending) >= freebuffAdEntries {
		var oldestKey string
		var oldest time.Time
		for k, v := range freebuffPending {
			if oldestKey == "" || v.expiresAt.Before(oldest) {
				oldestKey, oldest = k, v.expiresAt
			}
		}
		delete(freebuffPending, oldestKey)
	}
	freebuffPending[connID] = freebuffPendingAd{ad: ad, expiresAt: now.Add(freebuffAdTTL)}
}

func freebuffTakePendingAd(connID, adID string) (freebuffAd, bool) {
	freebuffAdMu.Lock()
	defer freebuffAdMu.Unlock()

	pending, ok := freebuffPending[connID]
	if !ok {
		return freebuffAd{}, false
	}
	if time.Now().After(pending.expiresAt) {
		delete(freebuffPending, connID)
		return freebuffAd{}, false
	}
	if pending.ad.AdID != adID {
		return freebuffAd{}, false
	}
	delete(freebuffPending, connID)
	return pending.ad, true
}

func freebuffResetAdsForTest() {
	freebuffAdMu.Lock()
	defer freebuffAdMu.Unlock()
	freebuffPending = make(map[string]freebuffPendingAd, freebuffAdEntries)
}
