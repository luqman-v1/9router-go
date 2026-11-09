package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/log"
	"9router/proxy/internal/proxy"
)

// The client-attestation headers chat.deepseek.com's web client sends.
//
// `x-hif-leim` and `x-hif-dliq` are signed values the browser fetches from two
// dedicated hosts and caches, keyed by the TTL the answer announces in
// `x-hif-ttl` (600s). They are not derived from the token, the request or the
// account, so fetching them the way the browser does reproduces the header
// exactly — this is not a signature to reverse engineer, it is a token to
// collect.
// The two hosts are package vars rather than constants so a test can point a
// whole turn at a stub: without that seam every test that drives a turn would
// have to reach the public internet to fetch an attestation value.
var (
	deepSeekHifLeimURL = "https://hif-leim.deepseek.com/query"
	deepSeekHifDliqURL = "https://hif-dliq.deepseek.com/query"
)

const (
	// deepSeekHifDefaultTTL is the fallback cache lifetime when the answer
	// carries no usable x-hif-ttl, matching the sibling implementation.
	deepSeekHifDefaultTTL = 600 * time.Second

	// deepSeekHifTimeout bounds one attestation fetch. It is a small JSON
	// answer from a dedicated host, and a turn must never wait long for it.
	deepSeekHifTimeout = 10 * time.Second

	// deepSeekHifMaxBodyBytes caps the answer read.
	deepSeekHifMaxBodyBytes = 64 * 1024
)

// deepSeekHifCache holds the two attestation values with their own expiries.
//
// A stale value is reused when a refresh fails: the site's own client keeps
// serving the cached value until its TTL is re-fetched, and a header that was
// accepted a minute ago is better evidence of a browser than a missing one.
type deepSeekHifCache struct {
	mu     sync.Mutex
	values map[string]deepSeekHifEntry
}

type deepSeekHifEntry struct {
	value     string
	expiresAt time.Time
}

var deepSeekHif = &deepSeekHifCache{values: make(map[string]deepSeekHifEntry)}

// headers returns the attestation headers, or an empty map when nothing could
// be fetched and nothing is cached. It never returns an error: the caller must
// be able to make its request regardless.
//
// The values are fetched through the turn when it has a browser-fidelity
// transport: the attestation hosts are part of the site's anti-bot pipeline,
// and the value they hand out is presented by the very next completion call.
// A value collected over Go's own TLS handshake contradicts the Chrome
// handshake that presents it, so the fetch rides the same stack the answer
// will.
func (c *deepSeekHifCache) headers(ctx context.Context, turn deepSeekTurn) map[string]string {
	out := map[string]string{}
	for _, host := range []struct {
		key   string
		url   string
		field string
	}{
		{key: "leim", url: deepSeekHifLeimURL, field: "x-hif-leim"},
		{key: "dliq", url: deepSeekHifDliqURL, field: "x-hif-dliq"},
	} {
		if turn.transport != nil {
			if value := c.resolve(ctx, host.key, func() (string, time.Duration) {
				return c.fetchViaTransport(ctx, turn, host.url)
			}); value != "" {
				out[host.field] = value
			}
			continue
		}
		if value := c.value(ctx, turn.client, host.key, host.url, turn.origin); value != "" {
			out[host.field] = value
		}
	}
	return out
}

// resolve is the cache discipline around one fetch strategy: a fresh value is
// returned as-is, a failed refresh falls back to the stale one, and a
// successful one is stored for the TTL it announced.
func (c *deepSeekHifCache) resolve(ctx context.Context, key string, fetch func() (string, time.Duration)) string {
	if cached, ok := c.fresh(key); ok {
		return cached
	}

	value, ttl := fetch()
	if value == "" {
		// A failed refresh keeps whatever is cached: a value the site issued is
		// closer to a browser than no header at all.
		return c.stale(key)
	}

	c.store(key, value, ttl)
	return value
}

// value returns one attestation value over the plain client, refreshing it
// when its TTL has passed.
func (c *deepSeekHifCache) value(ctx context.Context, client *http.Client, key, url, origin string) string {
	return c.resolve(ctx, key, func() (string, time.Duration) {
		return c.fetch(ctx, client, url, origin)
	})
}

func (c *deepSeekHifCache) fresh(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.values[key]
	if !ok || !time.Now().Before(entry.expiresAt) {
		return "", false
	}
	return entry.value, true
}

func (c *deepSeekHifCache) stale(key string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.values[key].value
}

func (c *deepSeekHifCache) store(key, value string, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] = deepSeekHifEntry{value: value, expiresAt: time.Now().Add(ttl)}
}

// fetch performs one attestation request over the plain client. Any failure —
// network, status, payload — degrades to an empty value, which the caller
// reads as "no header".
func (c *deepSeekHifCache) fetch(ctx context.Context, client *http.Client, url, origin string) (string, time.Duration) {
	if client == nil {
		client = http.DefaultClient
	}
	fetchCtx, cancel := context.WithTimeout(ctx, deepSeekHifTimeout)
	defer cancel()

	// The attestation hosts are CORS endpoints of the chat site, so the real
	// client sends its Origin. Sending it keeps the request indistinguishable
	// from the browser's own fetch.
	resp, err := proxy.DoRequest(fetchCtx, client, http.MethodGet, url, map[string]string{
		"Accept":             "*/*",
		"Accept-Language":    "en-US,en;q=0.9",
		"Origin":             origin,
		"User-Agent":         deepSeekWebUserAgent,
		"X-Client-Version":   deepSeekWebClientVersion,
		"X-Client-Platform":  "web",
		"X-Client-Bundle-Id": "com.deepseek.chat",
	}, nil)
	return c.readAttestation(resp, err, url)
}

// fetchViaTransport performs the same fetch over the turn's browser-fidelity
// transport, so the attestation host sees the handshake the completion call
// will present the collected value with.
//
// The header set is the browser's CORS GET: the fetch hints, the chat origin
// and referer, and the client block the site's own instrumentation carries.
// The transport writes them in its ordered, browser-shaped way; the cookie is
// the jar's business, and the jar holds none for these hosts.
func (c *deepSeekHifCache) fetchViaTransport(ctx context.Context, turn deepSeekTurn, url string) (string, time.Duration) {
	fetchCtx, cancel := context.WithTimeout(ctx, deepSeekHifTimeout)
	defer cancel()

	response, err := turn.transport.send(fetchCtx, http.MethodGet, url, nil, map[string]string{
		"accept":             "*/*",
		"accept-encoding":    "gzip, deflate, br, zstd",
		"accept-language":    "en-US,en;q=0.9",
		"origin":             turn.origin,
		"priority":           "u=1, i",
		"referer":            turn.origin + "/",
		"sec-fetch-dest":     "empty",
		"sec-fetch-mode":     "cors",
		"sec-fetch-site":     "same-site",
		"user-agent":         deepSeekWebUserAgent,
		"x-client-bundle-id": "com.deepseek.chat",
		"x-client-platform":  "web",
		"x-client-version":   deepSeekWebClientVersion,
	})
	if err != nil {
		log.Debug("deepseek-web", "attestation fetch failed", "url", url, "error", err)
		return "", 0
	}
	return c.readAttestation(response, nil, url)
}

// readAttestation reads one attestation answer: the value out of the payload,
// the cache lifetime out of x-hif-ttl. Any failure degrades to an empty value.
func (c *deepSeekHifCache) readAttestation(resp *http.Response, err error, url string) (string, time.Duration) {
	if err != nil {
		log.Debug("deepseek-web", "attestation fetch failed", "url", url, "error", err)
		return "", 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Debug("deepseek-web", "attestation host refused", "url", url, "status", resp.StatusCode)
		return "", 0
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, deepSeekHifMaxBodyBytes))
	if err != nil {
		log.Debug("deepseek-web", "attestation body unreadable", "url", url, "error", err)
		return "", 0
	}
	if value := deepSeekAttestationValue(body); value != "" {
		return value, deepSeekHifTTL(resp.Header.Get("x-hif-ttl"))
	}
	return "", 0
}

// deepSeekAttestationValue reads the issued value out of an attestation
// answer, or "" when the payload carries none.
func deepSeekAttestationValue(body []byte) string {
	var answer struct {
		Data struct {
			BizData struct {
				Value string `json:"value"`
			} `json:"biz_data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return ""
	}
	return strings.TrimSpace(answer.Data.BizData.Value)
}

// deepSeekHifTTL reads the announced cache lifetime, falling back to the
// documented default rather than to a zero TTL: a value fetched once should not
// be re-fetched on every turn of a conversation.
func deepSeekHifTTL(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return deepSeekHifDefaultTTL
	}
	parsed, err := time.ParseDuration(header + "s")
	if err != nil || parsed <= 0 {
		return deepSeekHifDefaultTTL
	}
	return parsed
}
