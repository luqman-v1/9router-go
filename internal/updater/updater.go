// Package updater handles app versioning, release update checks, and safe self-updating.
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/log"
)

// CurrentVersion is the active 9router-go application version.
// Can be set at build time via -ldflags "-X 9router/proxy/internal/updater.CurrentVersion=1.4.0"
var CurrentVersion = "1.4.0"

// DefaultUpdateURL is the default remote version manifest URL.
var DefaultUpdateURL = "https://raw.githubusercontent.com/luqman-v1/9router-go/main/version.json"

var (
	cachedInfo   *UpdateInfo
	cacheMu      sync.RWMutex
	lastCheckTime time.Time
)

// UpdateInfo holds version details and update status.
type UpdateInfo struct {
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	HasUpdate      bool   `json:"hasUpdate"`
	DownloadURL    string `json:"downloadUrl"`
	ReleaseNotes   string `json:"releaseNotes"`
	GoVersion      string `json:"goVersion"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	CheckedAt      string `json:"checkedAt"`
}

// GetCachedInfo returns the latest cached UpdateInfo or a default.
func GetCachedInfo() *UpdateInfo {
	cacheMu.RLock()
	defer cacheMu.RUnlock()

	if cachedInfo != nil {
		return cachedInfo
	}

	return &UpdateInfo{
		CurrentVersion: CurrentVersion,
		LatestVersion:  CurrentVersion,
		HasUpdate:      false,
		GoVersion:      runtime.Version(),
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		CheckedAt:      time.Now().UTC().Format(time.RFC3339),
	}
}

// CheckUpdate queries remote version endpoint and compares semver versions.
func CheckUpdate(ctx context.Context) (*UpdateInfo, error) {
	updateURL := os.Getenv("UPDATE_URL")
	if updateURL == "" {
		updateURL = DefaultUpdateURL
	}

	req, err := http.NewRequestWithContext(ctx, "GET", updateURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create update check request: %w", err)
	}

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch update info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update endpoint returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read update response body: %w", err)
	}

	var remote struct {
		Version      string            `json:"version"`
		DownloadURLs map[string]string `json:"downloadUrls"`
		ReleaseNotes string            `json:"releaseNotes"`
	}

	if err := json.Unmarshal(body, &remote); err != nil {
		return nil, fmt.Errorf("parse update JSON: %w", err)
	}

	latestVersion := strings.TrimPrefix(remote.Version, "v")
	current := strings.TrimPrefix(CurrentVersion, "v")
	hasUpdate := CompareVersions(latestVersion, current) > 0

	platformKey := fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH)
	downloadURL := remote.DownloadURLs[platformKey]
	if downloadURL == "" {
		downloadURL = remote.DownloadURLs["default"]
	}

	info := &UpdateInfo{
		CurrentVersion: CurrentVersion,
		LatestVersion:  latestVersion,
		HasUpdate:      hasUpdate,
		DownloadURL:    downloadURL,
		ReleaseNotes:   remote.ReleaseNotes,
		GoVersion:      runtime.Version(),
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		CheckedAt:      time.Now().UTC().Format(time.RFC3339),
	}

	cacheMu.Lock()
	cachedInfo = info
	lastCheckTime = time.Now()
	cacheMu.Unlock()

	return info, nil
}

// PerformSelfUpdate downloads and safely replaces the active executable binary.
func PerformSelfUpdate(downloadURL string) error {
	if downloadURL == "" {
		return fmt.Errorf("missing download URL for platform %s_%s", runtime.GOOS, runtime.GOARCH)
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable path: %w", err)
	}

	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("resolve symlink path: %w", err)
	}

	log.Info("updater", "starting download for auto-update", "url", downloadURL, "target", execPath)

	resp, err := http.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("download binary asset: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download binary returned status %d", resp.StatusCode)
	}

	// Create temporary binary file in target directory
	dir := filepath.Dir(execPath)
	tmpFile, err := os.CreateTemp(dir, ".9router-go-update-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write binary asset to temp file: %w", err)
	}
	tmpFile.Close()

	// Grant executable permissions (rwxr-xr-x)
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("chmod executable: %w", err)
	}

	// Replace active binary atomically
	oldPath := execPath + ".old"
	_ = os.Remove(oldPath)

	if err := os.Rename(execPath, oldPath); err != nil {
		return fmt.Errorf("backup active binary: %w", err)
	}

	if err := os.Rename(tmpPath, execPath); err != nil {
		// Rollback on failure
		_ = os.Rename(oldPath, execPath)
		return fmt.Errorf("swap binary asset: %w", err)
	}

	_ = os.Remove(oldPath)
	log.Info("updater", "auto-update completed successfully!", "binary", execPath)
	return nil
}

// StartBackgroundCheck initiates an async background update check and auto-updates if enabled.
func StartBackgroundCheck(autoUpdate bool) {
	go func() {
		// Wait 3 seconds after startup to not block initial gateway startup
		time.Sleep(3 * time.Second)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		info, err := CheckUpdate(ctx)
		if err != nil {
			log.Debug("updater", "check update skipped/failed", "error", err)
			return
		}

		if info.HasUpdate {
			log.Info("updater", "NEW UPDATE AVAILABLE!", "current", info.CurrentVersion, "latest", info.LatestVersion, "notes", info.ReleaseNotes)

			if autoUpdate || os.Getenv("AUTO_UPDATE") == "true" {
				log.Info("updater", "AUTO_UPDATE is enabled — initiating update...")
				if err := PerformSelfUpdate(info.DownloadURL); err != nil {
					log.Error("updater", "auto-update failed", "error", err)
				} else {
					log.Info("updater", "update complete! Restarting process...")
					restartSelf()
				}
			}
		} else {
			log.Debug("updater", "app is up to date", "version", info.CurrentVersion)
		}
	}()
}

func restartSelf() {
	execPath, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(execPath, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	_ = cmd.Start()
	os.Exit(0)
}

// CompareVersions compares two semver strings (v1 > v2 -> 1, v1 < v2 -> -1, v1 == v2 -> 0).
func CompareVersions(v1, v2 string) int {
	parts1 := parseSemver(v1)
	parts2 := parseSemver(v2)

	for i := 0; i < 3; i++ {
		if parts1[i] > parts2[i] {
			return 1
		}
		if parts1[i] < parts2[i] {
			return -1
		}
	}
	return 0
}

func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	var parts [3]int
	fmt.Sscanf(v, "%d.%d.%d", &parts[0], &parts[1], &parts[2])
	return parts
}
