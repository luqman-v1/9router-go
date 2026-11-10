package dashboard

// Free-pi's ad surface.
//
// free-pi pays for inference with advertising, and upstream gates completions on
// ad *impression records*: without them a request 403s with "Ads aren't showing
// for this account". The reference implementation satisfies that gate by
// automating impressions headlessly while rendering nothing — reporting ad views
// to the advertiser that no human ever saw, for the ads whose spend funds the
// inference. That is not implemented here.
//
// What is implemented instead: the creative is served to the dashboard, the
// dashboard renders it, and the impression is reported **only when the client
// says it painted the ad**. The server keeps a short-lived record of what it
// handed out, so an impression can only be recorded for a creative it actually
// served, and only once.
//
// Deliberate limitation, stated rather than papered over: free-pi's wire
// protocol also carries an `x-gravity-context` header, built by the npm
// `@gravity-ai/api` SDK from browser signals (user-agent, screen size, timezone,
// locale) plus a Gravity pixel session. It has no Go port, and a server cannot
// observe a screen size or a browser locale, so generating it here would mean
// inventing the very signals this file refuses to fake. It is therefore omitted
// from chat requests. Whether upstream still attributes an impression without it
// is UNVERIFIED — the gate itself is honoured, the attribution question is not
// answered, and that gap is visible here rather than hidden.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

const (
	// freePiAdTTL bounds how long a served creative may be reported on. Past it
	// the ad is stale and an impression would describe a view that has long
	// since scrolled away.
	freePiAdTTL = 15 * time.Minute

	// freePiAdEntries caps the served-creative table. It holds one entry per
	// connection with an ad on screen, so the cap is a backstop against a
	// misbehaving client rather than a working limit (AGENTS.md section 4.G).
	freePiAdEntries = 64

	// freePiAdsTimeout bounds the ad round trips. They are decoration, not
	// inference: a slow ad network must never hold a dashboard open.
	freePiAdsTimeout = 8 * time.Second
)

// freePiAdClient is the HTTP client for the ad endpoints. Same proxy-aware
// transport as the quota reader, for the same reason: a sandbox or corporate
// proxy otherwise turns every call into an opaque transport error.
var freePiAdClient = &http.Client{
	Transport: proxy.NewFallbackTransport(http.DefaultTransport),
	Timeout:   freePiAdsTimeout,
}

// freePiCreative is the renderable part of an ad.
type freePiCreative struct {
	Title    string `json:"title,omitempty"`
	Body     string `json:"body,omitempty"`
	CTA      string `json:"cta,omitempty"`
	ImageURL string `json:"imageUrl,omitempty"`
}

// freePiAd is one ad as served by GET /ads/next?slot=banner. Only AdID and
// ClickToken are needed to report the impression; the rest is for rendering.
type freePiAd struct {
	AdID string `json:"adId"`
	// ClickToken is deliberately not serialised: the impression is reported
	// server-side from the pending record, so the browser never needs it and it
	// should not leave the process.
	ClickToken string         `json:"-"`
	ImpURL     string         `json:"-"`
	ClickURL   string         `json:"clickUrl,omitempty"`
	Creative   freePiCreative `json:"creative"`
	Source     string         `json:"source,omitempty"`
	FetchedAt  time.Time      `json:"-"`
}

// freePiOriginFromBase maps a connection's base URL to the API root, so a
// relayed or stubbed endpoint serves its own ads rather than reaching production.
func freePiOriginFromBase(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	for _, suffix := range []string{"/v1/chat/completions", "/chat/completions", "/v1"} {
		if trimmed := strings.TrimSuffix(base, suffix); trimmed != base {
			return trimmed
		}
	}
	if base == "" {
		return providers.FreePiOrigin
	}
	return base
}

// freePiAdURLValidator is the guard applied to an ad's tracking pixel before it
// is fetched. A var so a test can point the pixel at a local server without
// weakening the rule: production always runs freePiAdURLAllowed, whose behaviour
// is pinned exhaustively by TestFreePiAdURLAllowed, and
// TestFreePiReportImpression_RefusesForeignPixel proves the guard is consulted.
var freePiAdURLValidator = freePiAdURLAllowed

// freePiAdURLAllowed reports whether an ad's tracking pixel may be fetched.
//
// imp_url arrives from upstream and is fetched server-side, so an unvalidated
// host is an SSRF primitive. The reference proxy applies the same check
// (server.mjs:350); the host list lives in providers so it is pinned by a
// contract test rather than duplicated as a literal here.
func freePiAdURLAllowed(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range providers.FreePiImpressionHosts {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
}

// freePiFetchAd asks upstream for a banner ad.
//
// A 204 (or 200 with no ad id) means there is nothing to show, which is a normal
// outcome and not an error: the dashboard simply renders no banner.
func freePiFetchAd(ctx context.Context, token, baseURL string) (*freePiAd, error) {
	endpoint := freePiOriginFromBase(baseURL) + "/ads/next?slot=banner"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("free-pi ad: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := freePiAdClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("free-pi ad: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("free-pi ad: HTTP %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("free-pi ad: read response: %w", err)
	}

	// The upstream field names are snake_case; the wire struct keeps them so a
	// rename on their side fails loudly here instead of silently rendering a
	// blank ad.
	var wire struct {
		AdID       string `json:"ad_id"`
		ClickToken string `json:"click_token"`
		ImpURL     string `json:"imp_url"`
		ClickURL   string `json:"click_url"`
		Source     string `json:"source"`
		Creative   struct {
			Title    string `json:"title"`
			Body     string `json:"body"`
			CTA      string `json:"cta"`
			ImageURL string `json:"image_url"`
		} `json:"creative"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("free-pi ad: parse response: %w", err)
	}
	if wire.AdID == "" {
		return nil, nil
	}

	return &freePiAd{
		AdID:       wire.AdID,
		ClickToken: wire.ClickToken,
		ImpURL:     wire.ImpURL,
		ClickURL:   wire.ClickURL,
		Source:     wire.Source,
		Creative: freePiCreative{
			Title:    wire.Creative.Title,
			Body:     wire.Creative.Body,
			CTA:      wire.Creative.CTA,
			ImageURL: wire.Creative.ImageURL,
		},
		FetchedAt: time.Now(),
	}, nil
}

// freePiReportImpression records one impression for an ad the dashboard painted:
// the Gravity pixel first (that is what the ad network measures), then the
// server-side record upstream gates on.
//
// Called only from the client's render event, never from a timer.
func freePiReportImpression(ctx context.Context, token, baseURL string, ad freePiAd) error {
	if ad.AdID == "" || ad.ClickToken == "" {
		return fmt.Errorf("free-pi ad: impression needs an ad id and click token")
	}

	if ad.ImpURL != "" {
		if !freePiAdURLValidator(ad.ImpURL) {
			// Refuse rather than follow an unexpected host. The impression is not
			// reported either: a pixel we would not fetch means the view was not
			// measured, and claiming it was is the misreport this file exists to
			// avoid.
			return fmt.Errorf("free-pi ad: refused impression pixel on a non-allowlisted host")
		}
		if err := freePiFirePixel(ctx, ad.ImpURL); err != nil {
			// A pixel failure is not fatal to the server-side record; the ad
			// network's own measurement is best-effort (the reference treats it
			// as fire-and-forget), so log and continue.
			log.Warn("free-pi", "impression pixel failed", "error", err)
		}
	}

	body, err := json.Marshal(map[string]any{
		"ad_id":       ad.AdID,
		"click_token": ad.ClickToken,
		"imp_fired":   true,
	})
	if err != nil {
		return fmt.Errorf("free-pi ad: marshal impression: %w", err)
	}

	endpoint := freePiOriginFromBase(baseURL) + "/ads/impression"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("free-pi ad: build impression request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := freePiAdClient.Do(req)
	if err != nil {
		return fmt.Errorf("free-pi ad: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("free-pi ad: impression HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

// freePiFirePixel sends the Gravity impression pixel.
func freePiFirePixel(ctx context.Context, impURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, impURL, nil)
	if err != nil {
		return err
	}
	resp, err := freePiAdClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return nil
}
