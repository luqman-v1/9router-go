package proxy_test

import (
	"strings"
	"testing"

	"9router/proxy/internal/proxy"
)

func TestBuildOpenCodeHeaders(t *testing.T) {
	headers := proxy.BuildOpenCodeHeaders(nil, "my-session-123", true)
	if headers["User-Agent"] != proxy.DefaultOpenCodeUA {
		t.Errorf("expected User-Agent %s, got %s", proxy.DefaultOpenCodeUA, headers["User-Agent"])
	}
	if headers["x-opencode-client"] != "cli" {
		t.Errorf("expected x-opencode-client cli, got %s", headers["x-opencode-client"])
	}
	if headers["x-opencode-project"] != "global" {
		t.Errorf("expected default x-opencode-project global like upstream, got %s", headers["x-opencode-project"])
	}
	if !proxy.OpenCodeSessionRegex.MatchString(headers["x-opencode-session"]) {
		t.Errorf("expected canonical x-opencode-session matching regex, got %s", headers["x-opencode-session"])
	}
	if len(headers["x-opencode-session"]) != 30 {
		t.Errorf("expected session length 30, got %d (%s)", len(headers["x-opencode-session"]), headers["x-opencode-session"])
	}
	if headers["x-opencode-request"] != "" {
		t.Errorf("v2 fingerprint must omit x-opencode-request, got %q", headers["x-opencode-request"])
	}
	for _, key := range []string{"x-opencode-session-id", "x-session-affinity", "x-session-id"} {
		if headers[key] != headers["x-opencode-session"] {
			t.Errorf("%s = %q, want session %q", key, headers[key], headers["x-opencode-session"])
		}
	}
	if headers["Accept"] != "text/event-stream" {
		t.Errorf("expected Accept text/event-stream, got %s", headers["Accept"])
	}

	// Non-stream accepts */*
	headersNonStream := proxy.BuildOpenCodeHeaders(nil, "", false)
	if headersNonStream["Accept"] != "*/*" {
		t.Errorf("expected Accept */*, got %s", headersNonStream["Accept"])
	}
}

func TestBuildOpenCodeHeaders_SessionHintMirrorsEverywhere(t *testing.T) {
	// A downstream session hint must move every mirror together: upstream
	// derives one session and sets all four headers from it.
	t.Run("canonical hint", func(t *testing.T) {
		hint := "ses_f52b0d414ffeObbCKcHUQYZR2I"
		headers := proxy.BuildOpenCodeHeaders(map[string]string{"x-opencode-session": hint}, "", true)
		for _, key := range []string{"x-opencode-session", "x-opencode-session-id", "x-session-affinity", "x-session-id"} {
			if headers[key] != hint {
				t.Errorf("%s = %q, want the downstream hint %q", key, headers[key], hint)
			}
		}
	})
	t.Run("foreign hint is translated once and mirrored", func(t *testing.T) {
		headers := proxy.BuildOpenCodeHeaders(map[string]string{"x-opencode-session": "uuid-1234"}, "", true)
		want := proxy.TranslateOpenCodeSessionID("uuid-1234", "")
		for _, key := range []string{"x-opencode-session", "x-opencode-session-id", "x-session-affinity", "x-session-id"} {
			if headers[key] != want {
				t.Errorf("%s = %q, want %q", key, headers[key], want)
			}
		}
	})
	t.Run("caller project wins over the global default", func(t *testing.T) {
		headers := proxy.BuildOpenCodeHeaders(map[string]string{"x-opencode-project": "proj-1"}, "", true)
		if headers["x-opencode-project"] != "proj-1" {
			t.Errorf("x-opencode-project = %q, want proj-1", headers["x-opencode-project"])
		}
	})
}

func TestOpenCodeCanonicalSession(t *testing.T) {
	for range 20 {
		ses := proxy.GenerateOpenCodeSessionID()
		if !proxy.OpenCodeSessionRegex.MatchString(ses) || len(ses) != 30 {
			t.Fatalf("invalid session ID generated: %s (len %d)", ses, len(ses))
		}

		req := proxy.GenerateOpenCodeRequestID()
		if !proxy.OpenCodeRequestRegex.MatchString(req) || len(req) != 30 {
			t.Fatalf("invalid request ID generated: %s (len %d)", req, len(req))
		}
	}

	// Preserves valid canonical session
	valid := "ses_f52b0d414ffeObbCKcHUQYZR2I"
	if translated := proxy.TranslateOpenCodeSessionID(valid, ""); translated != valid {
		t.Errorf("expected %s preserved, got %s", valid, translated)
	}

	// Translates foreign session to canonical 30 chars
	translated := proxy.TranslateOpenCodeSessionID("uuid-1234-5678-abcdef", "claude")
	if !proxy.OpenCodeSessionRegex.MatchString(translated) || len(translated) != 30 {
		t.Errorf("expected valid canonical session from foreign ID, got %s", translated)
	}
}

func TestBuildOpenCodeHeaders_TraceHeaders(t *testing.T) {
	headers := proxy.BuildOpenCodeHeaders(nil, "my-session-123", true)

	b3Val := headers["b3"]
	tpVal := headers["traceparent"]

	if b3Val == "" || len(b3Val) < 40 || strings.Count(b3Val, "-") != 3 {
		t.Errorf("invalid or missing b3 header value: %q", b3Val)
	}
	if tpVal == "" || !strings.HasPrefix(tpVal, "00-") || len(tpVal) != 55 {
		t.Errorf("invalid or missing traceparent header value: %q", tpVal)
	}

	customHeaders := map[string]string{
		"b3":          "custom-b3",
		"traceparent": "00-custom-traceparent-01",
	}
	overridden := proxy.BuildOpenCodeHeaders(customHeaders, "my-session-123", true)
	if overridden["b3"] != "custom-b3" {
		t.Errorf("b3 override failed: got %q", overridden["b3"])
	}
	if overridden["traceparent"] != "00-custom-traceparent-01" {
		t.Errorf("traceparent override failed: got %q", overridden["traceparent"])
	}
}

func TestHasValidOpenCodeVersion(t *testing.T) {
	cases := []struct {
		ua    string
		valid bool
	}{
		{"opencode", false},
		{"opencode/1.16.5", false},
		{"opencode/1.17.0", false},
		{"opencode/1.18.31", false},
		{"opencode/latest/2.0.0/cli", true},
		{"opencode/latest/2.0.26/cli", true},
		{"opencode/stable/2.0.26/desktop", true},
		{"prefix opencode/latest/2.0.26/cli", false},
		{"Claude-Code/1.0", false},
		{"curl/8.7.1", false},
	}
	for _, c := range cases {
		if got := proxy.HasValidOpenCodeVersion(c.ua); got != c.valid {
			t.Errorf("HasValidOpenCodeVersion(%q) = %v, want %v", c.ua, got, c.valid)
		}
	}
}

func TestIsCanonicalOpenCodeSession(t *testing.T) {
	for range 20 {
		if id := proxy.GenerateOpenCodeSessionID(); !proxy.IsCanonicalOpenCodeSession(id) {
			t.Fatalf("minted session %q is not canonical", id)
		}
	}
	tests := []struct {
		name, id string
		want     bool
	}{
		{"canonical hint", "ses_f52b0d414ffeObbCKcHUQYZR2I", true},
		{"ascending timestamp", "ses_000000000001AAAAAAAAAAAA", false},
		{"foreign hex", "ses_0123456789abAAAAAAAAAAAA", false},
		{"wrong prefix", "msg_f52b0d414ffeObbCKcHUQYZR2I", false},
		{"too short", "ses_f52b0d414ff", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := proxy.IsCanonicalOpenCodeSession(tt.id); got != tt.want {
				t.Errorf("IsCanonicalOpenCodeSession(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

func TestTranslateOpenCodeSessionIDReencodesForeignEncoding(t *testing.T) {
	// Right ses_ shape, wrong time encoding: the gate decodes the prefix and
	// answers an ascending or foreign value with 403 FreeTierError, so it must
	// be re-encoded instead of forwarded.
	foreign := "ses_000000000001ObbCKcHUQYZR2I"
	got := proxy.TranslateOpenCodeSessionID(foreign, "")
	if got == foreign {
		t.Fatalf("foreign-encoded session was forwarded verbatim: %s", got)
	}
	if !proxy.IsCanonicalOpenCodeSession(got) {
		t.Errorf("re-encoded session %q is not canonical", got)
	}
	if again := proxy.TranslateOpenCodeSessionID(foreign, ""); again != got {
		t.Errorf("re-encoding is not deterministic: %q then %q", got, again)
	}
}

func TestStableOpenCodeSessionID(t *testing.T) {
	first := proxy.StableOpenCodeSessionID("conn-a")
	if again := proxy.StableOpenCodeSessionID("conn-a"); again != first {
		t.Errorf("same key minted two sessions: %q then %q", first, again)
	}
	if !proxy.IsCanonicalOpenCodeSession(first) {
		t.Errorf("stable session %q is not canonical", first)
	}
	if other := proxy.StableOpenCodeSessionID("conn-b"); other == first {
		t.Errorf("different keys share one session: %q", first)
	}
}

// A spent-session rotation must retire exactly the session that failed: a
// mismatched id (a race an already-rotated request won) keeps the newer
// session and its fresh budget, the matching id swaps it for a fresh mint.
func TestExpireStableOpenCodeSession(t *testing.T) {
	id := proxy.StableOpenCodeSessionID("expire-a")

	if proxy.ExpireStableOpenCodeSession("expire-a", "ses_wrong") {
		t.Error("expiry with a mismatched id removed the entry")
	}
	if again := proxy.StableOpenCodeSessionID("expire-a"); again != id {
		t.Errorf("failed expiry still rotated: %q -> %q", id, again)
	}

	if !proxy.ExpireStableOpenCodeSession("expire-a", id) {
		t.Error("expiry with the matching id failed")
	}
	fresh := proxy.StableOpenCodeSessionID("expire-a")
	if fresh == id {
		t.Error("a retired stable session was reused")
	}
	if !proxy.IsCanonicalOpenCodeSession(fresh) {
		t.Errorf("rotated session %q is not canonical", fresh)
	}

	if proxy.ExpireStableOpenCodeSession("expire-unknown", id) {
		t.Error("expiry of an unknown key reported success")
	}
}
