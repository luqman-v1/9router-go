package chat

import (
	"net/http"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// A 429 FreeUsageLimitError is one provider's verdict on one egress. The same
// proxy stays usable for every other provider, which never received that
// verdict — so the cooldown key carries the provider. Keying on the proxy alone
// would silently shrink the pool for providers that did not refuse it, and the
// operator would see traffic leave from the host's real IP with nothing in the
// logs to explain why.
func TestProxyCooldownIsKeyedByProvider(t *testing.T) {
	resetProxyCooldownsForTest()
	t.Cleanup(resetProxyCooldownsForTest)

	const proxyURL = "socks5://user:pass@198.51.100.10:33951"
	now := time.Now()

	parkProxyCooling("opencode", proxyURL, time.Hour, now)

	if !proxyCoolingDown("opencode", proxyURL, now) {
		t.Error("the provider that refused the proxy must see it cooling")
	}
	if proxyCoolingDown("gemini", proxyURL, now) {
		t.Error("another provider must still be allowed to use the proxy")
	}
	// All three OpenCode spellings share one lane: the id an operator types is
	// not normalised, so a ceiling that missed an alias would clamp that lane.
	if !proxyCoolingDown("opencode-zen", proxyURL, now) {
		t.Error("an opencode alias must share the opencode cooldown")
	}
}

// A cooldown expires on its own. Expiry is lazy, so no goroutine has to run for
// the proxy to come back.
func TestProxyCooldownExpires(t *testing.T) {
	resetProxyCooldownsForTest()
	t.Cleanup(resetProxyCooldownsForTest)

	const proxyURL = "socks5://user:pass@198.51.100.10:33951"
	now := time.Now()

	parkProxyCooling("opencode", proxyURL, time.Minute, now)
	if !proxyCoolingDown("opencode", proxyURL, now.Add(30*time.Second)) {
		t.Fatal("still inside the window: the proxy must be cooling")
	}
	if proxyCoolingDown("opencode", proxyURL, now.Add(2*time.Minute)) {
		t.Error("past the window: the proxy must be usable again")
	}
}

// A refusal that names no window leaves the proxy alone. Inventing a duration
// would remove an egress from rotation on a guess.
func TestProxyCooldownIgnoresZeroWindow(t *testing.T) {
	resetProxyCooldownsForTest()
	t.Cleanup(resetProxyCooldownsForTest)

	const proxyURL = "socks5://user:pass@198.51.100.10:33951"
	now := time.Now()

	parkProxyCooling("opencode", proxyURL, 0, now)
	if proxyCoolingDown("opencode", proxyURL, now) {
		t.Error("a zero or negative window must not park the proxy")
	}

	// An empty URL has no key to park.
	parkProxyCooling("opencode", "", time.Hour, now)
	if proxyCoolingDown("opencode", "", now) {
		t.Error("an empty proxy url is not a cooldown entry")
	}
}

// A second refusal extends a window rather than shortening it, so concurrent
// requests cannot race the deadline backwards.
func TestProxyCooldownExtendsNeverShortens(t *testing.T) {
	resetProxyCooldownsForTest()
	t.Cleanup(resetProxyCooldownsForTest)

	const proxyURL = "socks5://user:pass@198.51.100.10:33951"
	now := time.Now()

	parkProxyCooling("opencode", proxyURL, 2*time.Hour, now)
	parkProxyCooling("opencode", proxyURL, time.Minute, now)

	if !proxyCoolingDown("opencode", proxyURL, now.Add(90*time.Minute)) {
		t.Error("a shorter second refusal must not shorten the existing window")
	}
}

// The map is bounded. A pool that keeps rotating through distinct URLs must not
// grow it without limit (AGENTS.md §4.G).
func TestProxyCooldownsStayBounded(t *testing.T) {
	resetProxyCooldownsForTest()
	t.Cleanup(resetProxyCooldownsForTest)

	now := time.Now()
	for i := 0; i < proxyCooldownEntries*3; i++ {
		parkProxyCooling("opencode", proxyURLForIndex(i), time.Hour, now)
	}

	proxyCooldownsMu.Lock()
	size := len(proxyCooldowns)
	proxyCooldownsMu.Unlock()

	if size > proxyCooldownEntries {
		t.Errorf("cooldown map holds %d entries, cap is %d", size, proxyCooldownEntries)
	}
}

// proxyURLForIndex builds a distinct proxy URL for the bounding test.
func proxyURLForIndex(i int) string {
	return "socks5://user:pass@10.0." + itoa(i/256) + "." + itoa(i%256) + ":1080"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// redactProxyURL removes credentials. The egress label lands on every usage row
// and every fallback log line, and a proxy password is a secret.
func TestRedactProxyURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "userinfo is removed",
			in:   "socks5://demoUser:demoPass@198.51.100.10:33951",
			want: "socks5://***@198.51.100.10:33951",
		},
		{
			name: "username only is removed too",
			in:   "socks5://someuser@proxy.example:1080",
			want: "socks5://***@proxy.example:1080",
		},
		{
			name: "no credentials passes through",
			in:   "http://proxy.example:8080",
			want: "http://proxy.example:8080",
		},
		{
			name: "empty stays empty",
			in:   "   ",
			want: "",
		},
		{
			// The scheme separator is not the last @, so a URL with no
			// userinfo must not be mistaken for one that has it.
			name: "at sign after the scheme only",
			in:   "http://proxy.example/path@2",
			want: "http://***@2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactProxyURL(tt.in); got != tt.want {
				t.Errorf("redactProxyURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The per-provider ceiling exists so OpenCode's hour-scale refusal window is not
// clamped to the 2h account cap, without moving that cap for every provider.
func TestProxyCooldownCeilingPerProvider(t *testing.T) {
	if got := proxyCooldownCeiling("opencode"); got != openCodeProxyMaxCooldown {
		t.Errorf("opencode ceiling = %v, want %v", got, openCodeProxyMaxCooldown)
	}
	if got := proxyCooldownCeiling("gemini"); got != maxResetCooldown {
		t.Errorf("gemini ceiling = %v, want the shared %v", got, maxResetCooldown)
	}
	if openCodeProxyMaxCooldown <= maxResetCooldown {
		t.Error("the opencode ceiling must exceed the generic cap to be worth having")
	}
}

// Recording is opt-in per provider. The map is provider-keyed so a later
// addition needs no migration, but today only the OpenCode lanes record.
func TestHasProxyCooldownSupport(t *testing.T) {
	for _, p := range []string{"opencode", "opencode-go", "opencode-zen"} {
		if !hasProxyCooldownSupport(p) {
			t.Errorf("%q must record proxy cooldowns", p)
		}
	}
	for _, p := range []string{"gemini", "deepseek", "openai", ""} {
		if hasProxyCooldownSupport(p) {
			t.Errorf("%q must not record proxy cooldowns yet", p)
		}
	}
}

// The refusal handler must park the egress it just used, with the wait the
// upstream named, clamped by the provider ceiling.
func TestParkRefusedProxyUsesUpstreamWait(t *testing.T) {
	resetProxyCooldownsForTest()
	t.Cleanup(resetProxyCooldownsForTest)

	const proxyURL = "socks5://user:pass@198.51.100.10:33951"
	now := time.Now()

	ue := &upstreamError{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"120"}},
	}
	connData := &ConnectionData{
		Provider:         "opencode",
		ProxyPoolID:      "rot20",
		ResolvedProxyURL: proxyURL,
	}

	parkRefusedProxy("opencode", connData, ue)

	if !proxyCoolingDown("opencode", proxyURL, now.Add(time.Minute)) {
		t.Error("the refused proxy must be cooling after the upstream's 120s window")
	}
}

// A refusal far longer than the ceiling is clamped, so a hostile or nonsensical
// Retry-After cannot park an egress indefinitely.
func TestParkRefusedProxyClampsToCeiling(t *testing.T) {
	resetProxyCooldownsForTest()
	t.Cleanup(resetProxyCooldownsForTest)

	const proxyURL = "socks5://user:pass@198.51.100.10:33951"
	now := time.Now()

	ue := &upstreamError{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"864000"}}, // 10 days
	}
	connData := &ConnectionData{Provider: "opencode", ResolvedProxyURL: proxyURL}

	parkRefusedProxy("opencode", connData, ue)

	if !proxyCoolingDown("opencode", proxyURL, now.Add(openCodeProxyMaxCooldown-time.Minute)) {
		t.Error("still cooling just inside the ceiling")
	}
	if proxyCoolingDown("opencode", proxyURL, now.Add(openCodeProxyMaxCooldown+time.Minute)) {
		t.Error("the 10-day Retry-After must be clamped to the opencode ceiling")
	}
}

// A provider that does not record cooldowns, or a request with no resolved
// proxy, parks nothing.
func TestParkRefusedProxyIgnoresUnsupportedProviderAndMissingProxy(t *testing.T) {
	resetProxyCooldownsForTest()
	t.Cleanup(resetProxyCooldownsForTest)

	now := time.Now()
	ue := &upstreamError{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"120"}},
	}

	parkRefusedProxy("gemini", &ConnectionData{
		Provider:         "gemini",
		ResolvedProxyURL: "http://proxy.example:8080",
	}, ue)
	proxyCooldownsMu.Lock()
	size := len(proxyCooldowns)
	proxyCooldownsMu.Unlock()
	if size != 0 {
		t.Errorf("an unsupported provider parked %d entries, want 0", size)
	}

	// No resolved URL means the refusal cannot be attributed to an egress.
	parkRefusedProxy("opencode", &ConnectionData{Provider: "opencode"}, ue)
	if proxyCoolingDown("opencode", "", now) {
		t.Error("a request with no proxy must not create a cooldown entry")
	}
}

// newCooldownRepo builds a handler with one pool holding the given URLs, so the
// resolver's rotation can be observed directly.
func newCooldownRepo(t *testing.T, poolID, poolType string, urls []string) (*ChatHandler, *http.Client, func()) {
	t.Helper()

	database, cleanup := setupChatTestDB(t)
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	)`); err != nil {
		cleanup()
		t.Fatalf("create proxyPools: %v", err)
	}

	data := `{"name":"cool","urls":[`
	for i, u := range urls {
		if i > 0 {
			data += ","
		}
		data += `"` + u + `"`
	}
	data += `],"type":"` + poolType + `","strategy":"round-robin"}`

	if _, err := database.Exec(`INSERT INTO proxyPools (id, isActive, testStatus, data, createdAt, updatedAt)
		VALUES (?, 1, 'unknown', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		poolID, data); err != nil {
		cleanup()
		t.Fatalf("insert pool: %v", err)
	}

	direct := &http.Client{Transport: &http.Transport{}}
	h := &ChatHandler{Repo: db.NewRepo(database), Client: direct}
	return h, direct, cleanup
}

// resolvedProxyURL reports which proxy the resolver picked for a connection.
func resolvedProxyURL(t *testing.T, h *ChatHandler, provider string) string {
	t.Helper()
	connData := &ConnectionData{Provider: provider, ProxyPoolID: "cool"}
	if _, err := h.getClientForConnection(connData); err != nil {
		t.Fatalf("resolve client: %v", err)
	}
	return connData.ResolvedProxyURL
}

// The resolver must not hand back a proxy that is parked for this provider. With
// one of two proxies parked, every draw must come from the healthy one — that is
// the whole point: without it, round-robin walks straight back into the egress
// the gate has already closed.
func TestResolverSkipsACoolingProxyForThatProvider(t *testing.T) {
	resetProxyCooldownsForTest()
	t.Cleanup(resetProxyCooldownsForTest)

	const refused = "socks5://user:pass@198.51.100.10:33951"
	const healthy = "socks5://user:pass@198.51.100.11:30259"

	h, _, cleanup := newCooldownRepo(t, "cool", "socks5", []string{refused, healthy})
	defer cleanup()

	parkProxyCooling("opencode", refused, time.Hour, time.Now())

	for i := 0; i < 6; i++ {
		if got := resolvedProxyURL(t, h, "opencode"); got != healthy {
			t.Fatalf("draw %d picked %q, want the healthy proxy %q", i, got, healthy)
		}
	}
}

// The same pool must still offer both proxies to a provider that never refused
// one. This is the case a proxy-only key would break, and it is the reason the
// key carries the provider.
func TestResolverStillOffersTheProxyToOtherProviders(t *testing.T) {
	resetProxyCooldownsForTest()
	t.Cleanup(resetProxyCooldownsForTest)

	const refused = "socks5://user:pass@198.51.100.10:33951"
	const healthy = "socks5://user:pass@198.51.100.11:30259"

	h, _, cleanup := newCooldownRepo(t, "cool", "socks5", []string{refused, healthy})
	defer cleanup()

	parkProxyCooling("opencode", refused, time.Hour, time.Now())

	sawRefused := false
	for i := 0; i < 6; i++ {
		if got := resolvedProxyURL(t, h, "gemini"); got == refused {
			sawRefused = true
		}
	}
	if !sawRefused {
		t.Error("gemini must still be offered the proxy opencode refused")
	}
}

// With every proxy parked the request still has to leave. Failing the turn, or
// falling back to the direct client, would both be worse than trying a parked
// proxy: the first refuses service, the second publishes the host's real IP.
func TestResolverFallsBackWhenEveryProxyIsCooling(t *testing.T) {
	resetProxyCooldownsForTest()
	t.Cleanup(resetProxyCooldownsForTest)

	urls := []string{
		"socks5://user:pass@198.51.100.10:33951",
		"socks5://user:pass@198.51.100.11:30259",
	}
	h, direct, cleanup := newCooldownRepo(t, "cool", "socks5", urls)
	defer cleanup()

	for _, u := range urls {
		parkProxyCooling("opencode", u, time.Hour, time.Now())
	}

	connData := &ConnectionData{Provider: "opencode", ProxyPoolID: "cool"}
	client, err := h.getClientForConnection(connData)
	if err != nil {
		t.Fatalf("an all-cooling pool must still resolve, got error: %v", err)
	}
	if client == direct {
		t.Fatal("an all-cooling pool must still dial a proxy, never the direct client")
	}
	if connData.ResolvedProxyURL == "" {
		t.Error("a proxy must still be named so a further refusal can be attributed")
	}
}
