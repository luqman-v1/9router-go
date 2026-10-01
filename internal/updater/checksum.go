package updater

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"9router/proxy/internal/log"
)

// checksumsAssetName is the manifest release.yml publishes next to every
// binary asset; nothing else in the Go code used to read it.
const checksumsAssetName = "SHA256SUMS.txt"

// releaseAsset mirrors the subset of the GitHub release asset payload the
// updater needs. A named type keeps the JSON and the selection helpers on one
// shape instead of an anonymous struct repeated at every call site.
type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// findReleaseAssetURL returns the download URL of the named asset, or "" when
// the release does not carry it.
func findReleaseAssetURL(assets []releaseAsset, name string) string {
	for _, asset := range assets {
		if strings.EqualFold(asset.Name, name) {
			return asset.BrowserDownloadURL
		}
	}
	return ""
}

// assetFileName derives the checksum-file entry name from a download URL.
func assetFileName(downloadURL string) string {
	path := downloadURL
	if idx := strings.IndexAny(path, "?#"); idx != -1 {
		path = path[:idx]
	}
	path = strings.TrimSuffix(path, "/")
	if idx := strings.LastIndex(path, "/"); idx != -1 {
		path = path[idx+1:]
	}
	if unescaped, err := url.PathUnescape(path); err == nil {
		return unescaped
	}
	return path
}

// parseChecksumLine reads one `sha256sum` output line. The digest is followed
// by whitespace and a filename; a "*" marks binary mode, which is why the
// separator cannot simply be trimmed off.
func parseChecksumLine(line string) (digest, name string) {
	fields := strings.Fields(line)
	if len(fields) != 2 {
		return "", ""
	}
	name = strings.TrimPrefix(fields[1], "*")
	if !isSHA256Hex(fields[0]) {
		return "", ""
	}
	return fields[0], name
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// lookupAssetSHA256 fetches the release checksum manifest and returns the
// digest recorded for downloadURL.
func lookupAssetSHA256(ctx context.Context, assets []releaseAsset, downloadURL string) (string, error) {
	manifestURL := findReleaseAssetURL(assets, checksumsAssetName)
	if manifestURL == "" {
		return "", fmt.Errorf("release has no %s asset", checksumsAssetName)
	}
	if downloadURL == "" {
		return "", fmt.Errorf("no binary asset selected for this platform")
	}

	body, err := fetchChecksumManifest(ctx, manifestURL)
	if err != nil {
		return "", err
	}

	wanted := assetFileName(downloadURL)
	var fallback string
	for line := range strings.Lines(string(body)) {
		digest, name := parseChecksumLine(line)
		if name == "" {
			continue
		}
		if strings.EqualFold(name, wanted) {
			return digest, nil
		}
		// Some Windows publish paths fold `./9router-go.exe` into the name
		// (CMake treats the backslash as an escape, not a separator).
		if fallback == "" && strings.EqualFold(strings.TrimPrefix(name, "./"), wanted) {
			fallback = digest
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", fmt.Errorf("%s has no entry for %s", checksumsAssetName, wanted)
}

func fetchChecksumManifest(ctx context.Context, manifestURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build checksum request: %w", err)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", checksumsAssetName, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s HTTP %d", checksumsAssetName, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", checksumsAssetName, err)
	}
	return body, nil
}

// logChecksumLookupFailure records why the checksum is missing. The check itself
// still succeeds: the user must see that an update exists and be able to read
// the notes even when the release ships no digest for their platform.
func logChecksumLookupFailure(release string, err error) {
	log.Warn("updater", "release checksum unavailable — update cannot be installed automatically",
		"latest", release, "error", err)
}
