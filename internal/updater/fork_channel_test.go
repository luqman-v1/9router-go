package updater

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// parityRejectTransport fails the test if any request leaves the process. The
// unconfigured-channel guard must refuse before the first network hop, so
// reaching this transport is the bug, not just a wrong error message.
type parityRejectTransport struct{ t *testing.T }

func (r parityRejectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.t.Errorf("unexpected update network: %s", req.URL.Host)
	return nil, context.Canceled
}

func TestParityUnconfiguredChannelMakesNoRequest(t *testing.T) {
	// Not parallel: it swaps package globals and the shared transport.
	t.Setenv("UPDATE_URL", "")
	t.Setenv("UPDATE_REPO", "")
	oldURL, oldRepo := DefaultUpdateURL, DefaultGitHubRepo
	oldTransport := http.DefaultTransport
	t.Cleanup(func() {
		DefaultUpdateURL, DefaultGitHubRepo = oldURL, oldRepo
		http.DefaultTransport = oldTransport
	})
	DefaultUpdateURL, DefaultGitHubRepo = "", ""
	http.DefaultTransport = parityRejectTransport{t}
	info, err := CheckUpdate(context.Background())
	if info != nil || err == nil || !strings.Contains(err.Error(), "update channel is not configured") {
		t.Fatalf("info=%v err=%v", info, err)
	}
}

// TestParityForkFallbackStaysOnConfiguredRepository pins the half-configured
// case the guard could otherwise hide: a callable channel whose manifest is
// dead must fall back to the CONFIGURED repo, never to whatever upstream's
// default happens to be.
func TestParityForkFallbackStaysOnConfiguredRepository(t *testing.T) {
	t.Setenv("UPDATE_URL", "")
	t.Setenv("UPDATE_REPO", "")
	oldURL, oldRepo, oldAPI, oldVersion := DefaultUpdateURL, DefaultGitHubRepo, githubReleasesAPI, CurrentVersion
	t.Cleanup(func() {
		DefaultUpdateURL, DefaultGitHubRepo = oldURL, oldRepo
		githubReleasesAPI, CurrentVersion = oldAPI, oldVersion
	})
	digest := strings.Repeat("a", 64)
	asset := "9router-go_" + runtime.GOOS + "_" + runtime.GOARCH
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest":
			http.NotFound(w, r)
		case "/repos/fixture/fork/releases/latest":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"tag_name":"v1.0.1-local.2","assets":[{"name":%q,"browser_download_url":%q},{"name":"SHA256SUMS.txt","browser_download_url":%q}]}`, asset, base+"/"+asset, base+"/SHA256SUMS.txt")
		case "/SHA256SUMS.txt":
			_, _ = fmt.Fprintf(w, "%s  %s\n", digest, asset)
		default:
			t.Errorf("unexpected update path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	base = srv.URL
	DefaultUpdateURL, DefaultGitHubRepo = base+"/manifest", "fixture/fork"
	githubReleasesAPI, CurrentVersion = base+"/repos/%s/releases/latest", "1.0.1-local.1"
	info, err := CheckUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Source != "github_releases" || info.DownloadURL != base+"/"+asset || info.SHA256 != digest || !info.HasUpdate {
		t.Fatalf("unexpected fork update: %+v", info)
	}
}

// TestParityHalfConfiguredChannelStillChecks pins the other side of the guard:
// a build that configures exactly one source has a channel and must be
// checked. Refusing it — the shape an `||` guard takes — silently disables
// update checking for every manifest-only or repo-only fork.
func TestParityHalfConfiguredChannelStillChecks(t *testing.T) {
	// Not parallel: it swaps the same package globals as the tests above.
	t.Setenv("UPDATE_URL", "")
	t.Setenv("UPDATE_REPO", "")
	oldURL, oldRepo, oldAPI, oldVersion := DefaultUpdateURL, DefaultGitHubRepo, githubReleasesAPI, CurrentVersion
	t.Cleanup(func() {
		DefaultUpdateURL, DefaultGitHubRepo = oldURL, oldRepo
		githubReleasesAPI, CurrentVersion = oldAPI, oldVersion
	})
	CurrentVersion = "1.0.1-local.1"

	t.Run("manifest only", func(t *testing.T) {
		// The manifest must carry a digest to be believed at all (see the
		// CheckUpdate comment); without one it is a lead, not an answer, and
		// this subtest would be measuring the fallback instead of the guard.
		digest := strings.Repeat("b", 64)
		var base string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/manifest" {
				t.Errorf("repo channel is unconfigured, so no repo request may leave: %s", r.URL.Path)
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"latestVersion":"1.0.2","downloadUrl":%q,"sha256":%q}`, base+"/asset", digest)
		}))
		defer srv.Close()
		base = srv.URL
		DefaultUpdateURL, DefaultGitHubRepo = base+"/manifest", ""

		info, err := CheckUpdate(context.Background())
		if err != nil {
			t.Fatalf("a manifest-only channel must be checked, got %v", err)
		}
		if info == nil || info.Source != "manifest" || !info.HasUpdate {
			t.Fatalf("unexpected manifest-only result: %+v", info)
		}
	})

	t.Run("repo only", func(t *testing.T) {
		digest := strings.Repeat("a", 64)
		asset := "9router-go_" + runtime.GOOS + "_" + runtime.GOARCH
		var base string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/repos/fixture/fork/releases/latest":
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"tag_name":"v1.0.2","assets":[{"name":%q,"browser_download_url":%q},{"name":"SHA256SUMS.txt","browser_download_url":%q}]}`, asset, base+"/"+asset, base+"/SHA256SUMS.txt")
			case "/SHA256SUMS.txt":
				_, _ = fmt.Fprintf(w, "%s  %s\n", digest, asset)
			default:
				t.Errorf("manifest channel is unconfigured, so no manifest request may leave: %s", r.URL.Path)
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()
		base = srv.URL
		DefaultUpdateURL, DefaultGitHubRepo = "", "fixture/fork"
		githubReleasesAPI = base + "/repos/%s/releases/latest"

		info, err := CheckUpdate(context.Background())
		if err != nil {
			t.Fatalf("a repo-only channel must be checked, got %v", err)
		}
		if info == nil || info.Source != "github_releases" || !info.HasUpdate {
			t.Fatalf("unexpected repo-only result: %+v", info)
		}
	})
}
