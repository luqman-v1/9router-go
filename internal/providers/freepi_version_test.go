package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompareFreePiVersions(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"equal", "0.7", "0.7", 0},
		{"padded equal", "0.7", "0.7.0", 0},
		// The case in the bug report: 0.7 is above 0.6.16, which a naive string
		// comparison gets backwards.
		{"minor beats patch", "0.7", "0.6.16", 1},
		{"patch below", "0.6.15", "0.6.16", -1},
		{"segment count", "0.6.2", "0.6.16", -1},
		{"leading v", "v0.7", "0.7", 0},
		{"prerelease suffix ignored", "0.7.1-rc2", "0.7.1", 0},
		{"build suffix ignored", "0.7.1+build", "0.7.1", 0},
		{"empty is lowest", "", "0.1", -1},
		{"non-numeric segment is zero", "0.x", "0.0", 0},
		{"major", "1.0", "0.99.99", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := compareFreePiVersions(tt.a, tt.b); got != tt.want {
				t.Errorf("compareFreePiVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// stubVersionEndpoint points the refresh at an httptest server returning body
// and restores the real endpoint when the test ends.
func stubVersionEndpoint(t *testing.T, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(SetFreePiClientVersionEndpointForTesting(srv.URL))
}

// The bug: a connection pinned at login to a version the server has since
// raised `min` past. The resolver must send something the gate accepts, from a
// container whose compiled-in fallback was already correct.
func TestResolveFreePiClientVersion_LiftsAStalePinOverTheFloor(t *testing.T) {
	ResetFreePiClientVersionForTesting()
	t.Cleanup(ResetFreePiClientVersionForTesting)
	stubVersionEndpoint(t, `{"min":"0.6.16","latest":"0.7.2"}`)

	got := ResolveFreePiClientVersion(context.Background(), nil, "0.6.10")
	if got != "0.7.2" {
		t.Errorf("resolved = %q, want the published latest 0.7.2", got)
	}
}

// The published release is authoritative. Two older assertions in this table
// allowed a stored or compiled value above `latest`; live 2026-11-28 disproved
// them (min=latest=0.6.28, npm has no 0.7, server answers 426 upgrade_required).
func TestResolveFreePiClientVersion_PrefersTheNewestCandidate(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		stored string
		want   string
	}{
		{
			// An unpublished pin must not reach the wire even when it is the
			// highest number: 0.7 was never released, 0.6.28 is.
			name:   "published latest caps a newer stored version",
			body:   `{"min":"0.6.2","latest":"0.6.28"}`,
			stored: "0.9",
			want:   "0.6.28",
		},
		{
			name:   "published latest is newer than stored",
			body:   `{"min":"0.6.2","latest":"0.8"}`,
			stored: "0.7",
			want:   "0.8",
		},
		{
			name:   "stored missing uses the published latest over a stale compiled-in fallback",
			body:   `{"min":"0.6.2","latest":"0.6.28"}`,
			stored: "",
			want:   "0.6.28",
		},
		{
			// A floor raised ahead of the npm publish still wins: min is the
			// server's explicit requirement, not a release claim.
			name:   "min outranks a lower latest",
			body:   `{"min":"1.0","latest":"0.9"}`,
			stored: "0.7",
			want:   "1.0",
		},
		{
			// The live 426 case: an invented 0.7 pin plus a compiled fallback
			// that has since been lowered to a real release.
			name:   "unpublished stored version is clamped to the live latest",
			body:   `{"min":"0.6.28","latest":"0.6.28"}`,
			stored: "0.7",
			want:   "0.6.28",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ResetFreePiClientVersionForTesting()
			t.Cleanup(ResetFreePiClientVersionForTesting)
			stubVersionEndpoint(t, tt.body)

			if got := ResolveFreePiClientVersion(context.Background(), nil, tt.stored); got != tt.want {
				t.Errorf("resolved = %q, want %q", got, tt.want)
			}
		})
	}
}

// An unreachable endpoint must not break the request: the stored value (or the
// compiled-in fallback) is still what goes on the wire. With no live feed there
// is no published ceiling, so the fallback is the best known real release.
func TestResolveFreePiClientVersion_SurvivesAFailedRefresh(t *testing.T) {
	tests := []struct {
		name   string
		stored string
		want   string
	}{
		{"compiled-in fallback beats a stale pin", "0.6.10", FreePiClientVersion},
		{"a newer pin is kept when no feed can contradict it", "0.9", "0.9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ResetFreePiClientVersionForTesting()
			t.Cleanup(ResetFreePiClientVersionForTesting)
			// Port 1 is closed, so the fetch fails fast without leaving the machine.
			t.Cleanup(SetFreePiClientVersionEndpointForTesting("http://127.0.0.1:1/client-version"))

			if got := ResolveFreePiClientVersion(context.Background(), nil, tt.stored); got != tt.want {
				t.Errorf("resolved = %q, want %q", got, tt.want)
			}
		})
	}
}

// The snapshot is cached, so a second resolve inside the TTL does not re-dial.
func TestResolveFreePiClientVersion_CachesWithinTheTTL(t *testing.T) {
	ResetFreePiClientVersionForTesting()
	t.Cleanup(ResetFreePiClientVersionForTesting)

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"min":"0.6.2","latest":"0.7"}`))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(SetFreePiClientVersionEndpointForTesting(srv.URL))

	for i := 0; i < 3; i++ {
		ResolveFreePiClientVersion(context.Background(), nil, "0.7")
	}
	if calls != 1 {
		t.Errorf("endpoint calls = %d, want 1 within the TTL", calls)
	}
}

// The 426 path must bypass the TTL: a stale snapshot is what caused the 426.
func TestRefreshFreePiClientVersion_BypassesTheTTL(t *testing.T) {
	ResetFreePiClientVersionForTesting()
	t.Cleanup(ResetFreePiClientVersionForTesting)

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"min":"0.6.16","latest":"0.8"}`))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(SetFreePiClientVersionEndpointForTesting(srv.URL))

	if err := RefreshFreePiClientVersion(context.Background(), nil); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := RefreshFreePiClientVersion(context.Background(), nil); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if calls != 2 {
		t.Errorf("endpoint calls = %d, want 2 (refresh ignores the TTL)", calls)
	}
	if snap := FreePiVersionSnapshotForTesting(); snap.Latest != "0.8" {
		t.Errorf("snapshot latest = %q, want 0.8", snap.Latest)
	}
}
