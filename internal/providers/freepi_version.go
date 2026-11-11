package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// free-pi's forced-update gate: `GET /client-version` publishes a `min` floor
// and refuses an x-client-version below it with HTTP 426.
//
// The version we send is normally the `latest` stored on the connection at
// login. That snapshot is the bug this file exists to fix: it is never
// refreshed, so when the server raises `min` past it, every request keeps
// answering 426 until the operator signs in again — from a container that is
// already up to date. Raising the compiled-in fallback (FreePiClientVersion)
// cannot help either, because the stored value outranks it.
//
// So the value on the wire is resolved per request from three sources, and the
// highest wins: the stored snapshot, the server's current `latest`, and the
// compiled-in floor. A stale pin can then never beat a current server value.
const (
	// FreePiClientVersionTTL bounds how long the published min/latest pair is
	// reused. The endpoint is unauthenticated and account-independent, so one
	// snapshot per process serves every connection.
	FreePiClientVersionTTL = 15 * time.Minute

	// freePiClientVersionTimeout bounds the refresh. It runs on the request
	// path (best effort), so a slow endpoint must not become a slow turn.
	freePiClientVersionTimeout = 5 * time.Second
)

// FreePiClientVersionInfo is the subset of GET /client-version this package
// uses. `min` is the forced-update floor; `latest` is what a current client
// sends.
type FreePiClientVersionInfo struct {
	Min    string `json:"min"`
	Latest string `json:"latest"`
}

var (
	// freePiVersionURL is the endpoint the refresh dials. A var rather than a
	// const built from FreePiOrigin so a test can point it at a stub: the unit
	// suite must not reach the network (AGENTS.md §5.C).
	freePiVersionURL = FreePiOrigin + "/client-version"

	freePiVersionMu       sync.Mutex
	freePiVersionSnapshot FreePiClientVersionInfo
	freePiVersionFetched  time.Time
	freePiVersionInFlight bool
)

// SetFreePiClientVersionEndpointForTesting redirects the refresh at a stub and
// returns a func that restores the real endpoint.
func SetFreePiClientVersionEndpointForTesting(url string) func() {
	freePiVersionMu.Lock()
	previous := freePiVersionURL
	freePiVersionURL = url
	freePiVersionMu.Unlock()
	return func() {
		freePiVersionMu.Lock()
		freePiVersionURL = previous
		freePiVersionMu.Unlock()
	}
}

// FetchFreePiClientVersion reads GET /client-version. Exported so the login
// flow can reuse it rather than keeping a second copy of the shape.
func FetchFreePiClientVersion(ctx context.Context, client *http.Client) (FreePiClientVersionInfo, error) {
	freePiVersionMu.Lock()
	endpoint := freePiVersionURL
	freePiVersionMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return FreePiClientVersionInfo{}, fmt.Errorf("free-pi client-version request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	if client == nil {
		client = &http.Client{Timeout: freePiClientVersionTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return FreePiClientVersionInfo{}, fmt.Errorf("free-pi client-version: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return FreePiClientVersionInfo{}, fmt.Errorf("free-pi client-version: HTTP %d", resp.StatusCode)
	}

	var out FreePiClientVersionInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return FreePiClientVersionInfo{}, fmt.Errorf("free-pi client-version: parse: %w", err)
	}
	return out, nil
}

// ResolveFreePiClientVersion returns the x-client-version to send: the highest
// of the connection's stored snapshot, the server's published `latest`, and the
// compiled-in floor, but never below the server's `min`.
//
// A stale refresh is attempted inline but never fails the request: on a network
// error the last snapshot (or the compiled-in floor) still answers, and the 426
// self-heal in the executor retries once after a forced refresh.
func ResolveFreePiClientVersion(ctx context.Context, client *http.Client, stored string) string {
	snapshot := freePiVersionSnapshotOrRefresh(ctx, client)

	chosen := FreePiClientVersion
	for _, candidate := range []string{stored, snapshot.Latest, FreePiClientVersion} {
		if compareFreePiVersions(candidate, chosen) > 0 {
			chosen = candidate
		}
	}
	// The server refuses anything under min, so a snapshot that names a floor
	// above every candidate has to win: this is the forced-update case.
	if snapshot.Min != "" && compareFreePiVersions(chosen, snapshot.Min) < 0 {
		chosen = snapshot.Min
		if compareFreePiVersions(snapshot.Latest, chosen) > 0 {
			chosen = snapshot.Latest
		}
	}
	return chosen
}

// RefreshFreePiClientVersion forces a fetch, ignoring the TTL. It is the 426
// path: the server has just told us our version is too old, so the cached
// snapshot is by definition stale. A failure leaves the previous snapshot in
// place and is reported to the caller.
func RefreshFreePiClientVersion(ctx context.Context, client *http.Client) error {
	ctx, cancel := context.WithTimeout(ctx, freePiClientVersionTimeout)
	defer cancel()

	snapshot, err := FetchFreePiClientVersion(ctx, client)
	if err != nil {
		return err
	}

	freePiVersionMu.Lock()
	defer freePiVersionMu.Unlock()
	freePiVersionSnapshot = snapshot
	freePiVersionFetched = time.Now()
	return nil
}

// freePiVersionSnapshotOrRefresh returns the cached snapshot, refreshing it when
// the TTL has lapsed.
//
// The fetch runs without the lock held, and only one attempt is in flight at a
// time: a burst of requests past the TTL reuses the last snapshot instead of
// queueing on the mutex or stampeding the endpoint. That is the bounded,
// goroutine-free shape §4.G asks for — one snapshot, one flag, no map.
func freePiVersionSnapshotOrRefresh(ctx context.Context, client *http.Client) FreePiClientVersionInfo {
	freePiVersionMu.Lock()
	snapshot := freePiVersionSnapshot
	fresh := time.Since(freePiVersionFetched) < FreePiClientVersionTTL
	if fresh || freePiVersionInFlight {
		freePiVersionMu.Unlock()
		return snapshot
	}
	freePiVersionInFlight = true
	freePiVersionMu.Unlock()

	fetched, err := FetchFreePiClientVersion(ctx, client)

	freePiVersionMu.Lock()
	defer freePiVersionMu.Unlock()
	freePiVersionInFlight = false
	if err != nil {
		// Keep the previous snapshot; a failed refresh is not a failed request.
		return freePiVersionSnapshot
	}
	freePiVersionSnapshot = fetched
	freePiVersionFetched = time.Now()
	return fetched
}

// freePiVersionSnapshotForTesting returns the cached snapshot without touching
// the network, so a test can assert what the resolver stored.
func FreePiVersionSnapshotForTesting() FreePiClientVersionInfo {
	freePiVersionMu.Lock()
	defer freePiVersionMu.Unlock()
	return freePiVersionSnapshot
}

// ResetFreePiClientVersionForTesting clears the cache. Test-only seam for the
// same reason the other process-global caches have one: state leaking between
// tests makes them fail on the cache rather than on the behaviour.
func ResetFreePiClientVersionForTesting() {
	freePiVersionMu.Lock()
	defer freePiVersionMu.Unlock()
	freePiVersionSnapshot = FreePiClientVersionInfo{}
	freePiVersionFetched = time.Time{}
	freePiVersionInFlight = false
}

// compareFreePiVersions compares dotted numeric versions the way the forced
// update gate does: 0.7 > 0.6.16, and a missing segment counts as zero.
//
// It is deliberately local rather than shared with internal/updater: that
// package owns release-tag semantics (pre-release precedence and build
// metadata) for this repo's own versions, and this compares a vendor's plain
// dotted numbers. Two rules, two owners.
func compareFreePiVersions(a, b string) int {
	aParts := freePiVersionSegments(a)
	bParts := freePiVersionSegments(b)
	for i := 0; i < len(aParts) || i < len(bParts); i++ {
		var av, bv int
		if i < len(aParts) {
			av = aParts[i]
		}
		if i < len(bParts) {
			bv = bParts[i]
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

// freePiVersionSegments splits a dotted version into numeric segments, dropping
// any pre-release or build suffix ("0.7.1-rc2" → [0 7 1]).
func freePiVersionSegments(v string) []int {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if cut := strings.IndexAny(v, "-+"); cut >= 0 {
		v = v[:cut]
	}
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			// A non-numeric segment makes the whole comparison meaningless;
			// treating it as zero keeps us from claiming a version is newer
			// than it is.
			n = 0
		}
		out = append(out, n)
	}
	return out
}
