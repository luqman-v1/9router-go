// Package updater handles app versioning, release update checks, safe self-updating, and background auto-update.
package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/daemon"
	"9router/proxy/internal/log"
	"9router/proxy/internal/shutdown"
	"github.com/samber/lo"
)

// CurrentVersion is the active 9router-go application version.
// Can be overridden at build time via -ldflags "-X 9router/proxy/internal/updater.CurrentVersion=1.8.8"
// Default fallback is read from version.json at init if not overridden.
var CurrentVersion = "1.9.10-exp.3"

// DefaultUpdateURL is the primary remote version manifest URL.
var DefaultUpdateURL = "https://raw.githubusercontent.com/luqman-v1/9router-go/main/version.json"

// DefaultGitHubRepo is the repository for GitHub Releases API fallback.
var DefaultGitHubRepo = "luqman-v1/9router-go"

// DefaultCheckInterval is the periodic background update check interval (6 hours).
const DefaultCheckInterval = 6 * time.Hour

var (
	cachedInfo        *UpdateInfo
	cacheMu           sync.RWMutex
	lastCheckTime     time.Time
	autoUpdateEnabled bool
	autoUpdateMu      sync.RWMutex
	updateInProgress  bool
	updateProgressMu  sync.Mutex
)

// UpdateInfo holds detailed version and asset information.
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
	SHA256         string `json:"sha256,omitempty"`
	Source         string `json:"source,omitempty"` // "manifest" or "github_releases"
}

// UpdaterStatus represents the live background auto-update engine status.
type UpdaterStatus struct {
	CurrentVersion    string      `json:"currentVersion"`
	LatestVersion     string      `json:"latestVersion"`
	HasUpdate         bool        `json:"hasUpdate"`
	AutoUpdateEnabled bool        `json:"autoUpdateEnabled"`
	UpdateInProgress  bool        `json:"updateInProgress"`
	LastCheckTime     string      `json:"lastCheckTime,omitempty"`
	CheckInterval     string      `json:"checkInterval"`
	CachedInfo        *UpdateInfo `json:"cachedInfo,omitempty"`
}

// SetAutoUpdate sets the auto-update flag in memory.
func SetAutoUpdate(enabled bool) {
	autoUpdateMu.Lock()
	defer autoUpdateMu.Unlock()
	autoUpdateEnabled = enabled
}

// IsAutoUpdateEnabled returns whether auto-update is currently active.
func IsAutoUpdateEnabled() bool {
	autoUpdateMu.RLock()
	defer autoUpdateMu.RUnlock()
	return autoUpdateEnabled
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

// GetStatus returns the complete updater subsystem status.
func GetStatus() *UpdaterStatus {
	cacheMu.RLock()
	info := cachedInfo
	lastCheck := ""
	if !lastCheckTime.IsZero() {
		lastCheck = lastCheckTime.UTC().Format(time.RFC3339)
	}
	cacheMu.RUnlock()

	updateProgressMu.Lock()
	inProg := updateInProgress
	updateProgressMu.Unlock()

	latestVer := CurrentVersion
	hasUp := false
	if info != nil {
		latestVer = info.LatestVersion
		hasUp = info.HasUpdate
	}

	return &UpdaterStatus{
		CurrentVersion:    CurrentVersion,
		LatestVersion:     latestVer,
		HasUpdate:         hasUp,
		AutoUpdateEnabled: IsAutoUpdateEnabled(),
		UpdateInProgress:  inProg,
		LastCheckTime:     lastCheck,
		CheckInterval:     DefaultCheckInterval.String(),
		CachedInfo:        info,
	}
}

// githubReleasesAPI is the GitHub Releases endpoint template, overridden by the
// UPSTREAM_API_BASE release pipeline. It stays a template with a single verb so
// a test can point the fallback at a local server.
var githubReleasesAPI = lo.CoalesceOrEmpty(os.Getenv("UPSTREAM_API_BASE"), "https://api.github.com") + "/repos/%s/releases/latest"

// CheckUpdate queries remote version sources (manifest or GitHub Releases API) and compares semver.
//
// The manifest is tried first, but it is only allowed to answer when it can
// actually produce an installable update. Since #72 made the digest mandatory,
// a manifest that carries no sha256 is a dead end: PerformSelfUpdate refuses
// every install that reaches it, and the fallback that CAN fill the digest from
// the release's SHA256SUMS.txt would never run. version.json ships exactly
// three keys — downloadUrl, latestVersion, releaseNotes — so taking it on trust
// made the checksum path unreachable in production and broke `9router-go
// update` outright. A manifest without a digest therefore falls through to the
// GitHub Releases API, and its version metadata is used only if that fails too.
func CheckUpdate(ctx context.Context) (*UpdateInfo, error) {
	updateURL := lo.CoalesceOrEmpty(os.Getenv("UPDATE_URL"), DefaultUpdateURL)

	// 1. Try manifest URL first
	info, manifestErr := checkManifest(ctx, updateURL)
	if manifestErr == nil && info != nil {
		if info.SHA256 != "" || !info.HasUpdate {
			cacheUpdateInfo(info)
			return info, nil
		}
		// A manifest that advertises an update it cannot describe with a digest
		// is not an answer, only a lead. Keep it as the fallback so an outage
		// in the Releases API still shows the notes.
		log.Debug("updater", "manifest has no sha256, trying github releases", "url", updateURL)
		manifestErr = fmt.Errorf("manifest carries no sha256 for %s", info.LatestVersion)
	}

	// 2. Fallback to GitHub Releases API
	repo := lo.CoalesceOrEmpty(os.Getenv("UPDATE_REPO"), DefaultGitHubRepo)
	ghURL := fmt.Sprintf(githubReleasesAPI, repo)
	log.Debug("updater", "checking github releases fallback", "repo", repo)

	ghInfo, ghErr := checkGitHubReleases(ctx, ghURL)
	if ghErr == nil && ghInfo != nil {
		cacheUpdateInfo(ghInfo)
		return ghInfo, nil
	}

	if manifestErr != nil {
		return nil, fmt.Errorf("check update failed: manifest error (%w), github releases error (%w)", manifestErr, ghErr)
	}
	return nil, ghErr
}

func cacheUpdateInfo(info *UpdateInfo) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cachedInfo = info
	lastCheckTime = time.Now()
}

func checkManifest(ctx context.Context, url string) (*UpdateInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var remote struct {
		Version       string            `json:"version"`
		LatestVersion string            `json:"latestVersion"`
		DownloadURL   string            `json:"downloadUrl"`
		DownloadURLs  map[string]string `json:"downloadUrls"`
		ReleaseNotes  string            `json:"releaseNotes"`
		SHA256        string            `json:"sha256"`
	}
	if err := json.Unmarshal(body, &remote); err != nil {
		return nil, err
	}

	versionStr := remote.LatestVersion
	if versionStr == "" {
		versionStr = remote.Version
	}
	if versionStr == "" {
		return nil, fmt.Errorf("manifest missing version field")
	}

	latestVersion := strings.TrimPrefix(versionStr, "v")
	current := strings.TrimPrefix(CurrentVersion, "v")
	hasUpdate := CompareVersions(latestVersion, current) > 0

	platformKey := fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH)
	downloadURL := ""
	if remote.DownloadURLs != nil {
		downloadURL = remote.DownloadURLs[platformKey]
		if downloadURL == "" {
			downloadURL = remote.DownloadURLs["default"]
		}
	}
	if downloadURL == "" {
		downloadURL = remote.DownloadURL
	}

	return &UpdateInfo{
		CurrentVersion: CurrentVersion,
		LatestVersion:  latestVersion,
		HasUpdate:      hasUpdate,
		DownloadURL:    downloadURL,
		ReleaseNotes:   remote.ReleaseNotes,
		GoVersion:      runtime.Version(),
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		CheckedAt:      time.Now().UTC().Format(time.RFC3339),
		SHA256:         remote.SHA256,
		Source:         "manifest",
	}, nil
}

func checkGitHubReleases(ctx context.Context, apiURL string) (*UpdateInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "9router-go/"+CurrentVersion)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github releases HTTP %d", resp.StatusCode)
	}

	var release struct {
		TagName string         `json:"tag_name"`
		Name    string         `json:"name"`
		Body    string         `json:"body"`
		Assets  []releaseAsset `json:"assets"`
	}

	if err := json.UnmarshalRead(resp.Body, &release); err != nil {
		return nil, err
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	current := strings.TrimPrefix(CurrentVersion, "v")
	hasUpdate := CompareVersions(latestVersion, current) > 0

	downloadURL := matchReleaseAsset(release.Assets, runtime.GOOS, runtime.GOARCH)

	// A missing digest only disables the install: PerformSelfUpdate refuses to
	// rename over the running binary without one, and the user can still see
	// the release notes and update manually.
	sha256 := ""
	if digest, err := lookupAssetSHA256(ctx, release.Assets, downloadURL); err != nil {
		logChecksumLookupFailure(latestVersion, err)
	} else {
		sha256 = digest
	}

	return &UpdateInfo{
		CurrentVersion: CurrentVersion,
		LatestVersion:  latestVersion,
		HasUpdate:      hasUpdate,
		DownloadURL:    downloadURL,
		ReleaseNotes:   release.Body,
		GoVersion:      runtime.Version(),
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		SHA256:         sha256,
		CheckedAt:      time.Now().UTC().Format(time.RFC3339),
		Source:         "github_releases",
	}, nil
}

func archAliases(archKey string) []string {
	archKey = strings.ToLower(archKey)
	switch archKey {
	case "amd64", "x86_64", "x64":
		return []string{"amd64", "x86_64", "x64"}
	case "arm64", "aarch64":
		return []string{"arm64", "aarch64"}
	case "386", "x86", "i386":
		return []string{"386", "x86", "i386"}
	default:
		return []string{archKey}
	}
}

// matchReleaseAsset finds the best matching asset URL for target OS and Architecture.
func matchReleaseAsset(assets []releaseAsset, targetOS, targetArch string) string {
	osKey := strings.ToLower(targetOS)
	archNames := archAliases(targetArch)

	for _, asset := range assets {
		name := strings.ToLower(asset.Name)
		// Check OS match
		if !strings.Contains(name, osKey) {
			continue
		}
		// Check Arch match
		for _, arch := range archNames {
			if strings.Contains(name, arch) {
				return asset.BrowserDownloadURL
			}
		}
	}

	// Fallback to first non-checksum asset
	for _, asset := range assets {
		name := strings.ToLower(asset.Name)
		if !strings.HasSuffix(name, ".sha256") && !strings.HasSuffix(name, ".md5") && !strings.HasSuffix(name, ".txt") {
			return asset.BrowserDownloadURL
		}
	}
	return ""
}

// selfExecutable resolves the binary this process runs, so the tests can point
// the swap at a temp file instead of the live test binary.
var selfExecutable = os.Executable

// PerformSelfUpdate downloads, decompresses (tar.gz/zip if needed), verifies, and safely replaces the active binary.
// The downloaded asset (after archive extraction) is verified against expectedSHA256 before the binary is written
// to disk, and a mismatch aborts the update and keeps the running binary intact.
func PerformSelfUpdate(downloadURL, expectedSHA256 string) error {
	if downloadURL == "" {
		return fmt.Errorf("missing download URL for platform %s_%s", runtime.GOOS, runtime.GOARCH)
	}
	// Renaming over the running executable with no digest is exactly the
	// unverified install this refuses, so it is checked before any request.
	if expectedSHA256 == "" {
		return fmt.Errorf("refusing to install %s: release carries no checksum", downloadURL)
	}

	updateProgressMu.Lock()
	if updateInProgress {
		updateProgressMu.Unlock()
		return fmt.Errorf("update already in progress")
	}
	updateInProgress = true
	updateProgressMu.Unlock()

	defer func() {
		updateProgressMu.Lock()
		updateInProgress = false
		updateProgressMu.Unlock()
	}()

	execPath, err := selfExecutable()
	if err != nil {
		return fmt.Errorf("locate executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("resolve symlink path: %w", err)
	}

	log.Info("updater", "downloading update asset", "url", downloadURL, "target", execPath)

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("download binary asset: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download asset returned status %d", resp.StatusCode)
	}

	rawBytes, err := io.ReadAll(io.LimitReader(resp.Body, 150<<20)) // 150MB limit
	if err != nil {
		return fmt.Errorf("read downloaded asset: %w", err)
	}

	// Extract executable bytes from archive or raw binary
	binaryBytes, err := extractExecutableBytes(rawBytes, downloadURL)
	if err != nil {
		return fmt.Errorf("extract executable: %w", err)
	}

	actualSHA := ComputeSHA256(binaryBytes)
	if !strings.EqualFold(actualSHA, expectedSHA256) {
		return fmt.Errorf("SHA256 mismatch: expected %s, got %s", expectedSHA256, actualSHA)
	}
	log.Info("updater", "SHA256 checksum verified", "sha256", actualSHA)

	// Create temporary binary file in the target directory
	dir := filepath.Dir(execPath)
	tmpFile, err := os.CreateTemp(dir, ".9router-go-update-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(binaryBytes); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write binary to temp file: %w", err)
	}
	tmpFile.Close()

	// Ensure executable permissions (0755)
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("chmod executable: %w", err)
	}

	// Replace the running binary. The old image moves aside first so the rename
	// below can restore it if it fails, and the fresh image is then renamed to
	// the real path: renaming over a path this process is still executing from
	// fails on windows, which is the platform that needs it.
	oldPath := execPath + ".old"
	newPath := execPath + ".new"
	_ = os.Remove(oldPath)
	_ = os.Remove(newPath)

	if err := os.Rename(execPath, oldPath); err != nil {
		return fmt.Errorf("backup active binary: %w", err)
	}

	if err := os.Rename(tmpPath, newPath); err != nil {
		_ = os.Rename(oldPath, execPath)
		return fmt.Errorf("stage updated binary: %w", err)
	}

	if err := os.Rename(newPath, execPath); err != nil {
		_ = os.Rename(oldPath, execPath)
		_ = os.Remove(newPath)
		return fmt.Errorf("swap binary asset: %w", err)
	}

	_ = os.Remove(oldPath)
	log.Info("updater", "self-update applied successfully!", "binary", execPath)
	return nil
}

// extractExecutableBytes handles .tar.gz, .zip, and raw binaries.
// Uses a scoring heuristic to prefer the actual executable binary over
// README, LICENSE, checksum, or other non-executable files in the archive.
func extractExecutableBytes(data []byte, filenameOrURL string) ([]byte, error) {
	lower := strings.ToLower(filenameOrURL)

	// 1. Handle .tar.gz or .tgz (or gzip magic bytes)
	if strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz") || isGzip(data) {
		gzReader, err := gzip.NewReader(bytes.NewReader(data))
		if err == nil {
			defer gzReader.Close()
			tarReader := tar.NewReader(gzReader)
			var best []byte
			bestScore := 0
			for {
				header, err := tarReader.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					return nil, fmt.Errorf("read tar entry: %w", err)
				}
				if header.Typeflag != tar.TypeReg {
					continue
				}
				extracted, err := io.ReadAll(tarReader)
				if err != nil {
					continue
				}
				score := scoreArchiveEntry(header.Name, extracted)
				if score > bestScore {
					bestScore = score
					best = extracted
				}
			}
			if bestScore >= 3 && len(best) > 1024 {
				return best, nil
			}
		}
	}

	// 2. Handle .zip (or ZIP magic bytes)
	if strings.HasSuffix(lower, ".zip") || isZip(data) {
		zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err == nil {
			var best []byte
			bestScore := 0
			for _, file := range zipReader.File {
				if file.FileInfo().IsDir() {
					continue
				}
				rc, err := file.Open()
				if err != nil {
					continue
				}
				extracted, err := io.ReadAll(rc)
				rc.Close()
				if err != nil {
					continue
				}
				score := scoreArchiveEntry(file.Name, extracted)
				if score > bestScore {
					bestScore = score
					best = extracted
				}
			}
			if bestScore >= 3 && len(best) > 1024 {
				return best, nil
			}
		}
	}

	// 3. Raw executable binary — apply magic byte check
	if isELF(data) || isMachO(data) || isPE(data) {
		return data, nil
	}
	return data, nil
}

// scoreArchiveEntry returns a confidence score for an archive entry being the
// right binary. Higher = more likely to be the target executable.
func scoreArchiveEntry(name string, content []byte) int {
	score := 0
	low := strings.ToLower(name)

	// Prefer entries containing the project name over generic files
	if strings.Contains(low, "9router-go") || strings.Contains(low, "9router_go") {
		score += 10
	}

	// Match target OS
	if strings.Contains(low, runtime.GOOS) {
		score += 3
	}

	// Match target architecture
	for _, a := range archAliases(runtime.GOARCH) {
		if strings.Contains(low, strings.ToLower(a)) {
			score += 3
			break
		}
	}

	// Penalize non-executable extensions
	if strings.HasSuffix(low, ".md") || strings.HasSuffix(low, ".txt") ||
		strings.HasSuffix(low, ".sha256") || strings.HasSuffix(low, ".md5") ||
		strings.HasSuffix(low, ".yaml") || strings.HasSuffix(low, ".yml") ||
		strings.HasSuffix(low, ".json") || strings.HasSuffix(low, ".toml") {
		score -= 5
	}

	// Penalize well-known doc/asset names
	base := strings.TrimSuffix(low, ".exe")
	if base == "readme" || base == "license" || base == "changelog" ||
		base == "contributing" || base == "version" || base == "manifest" {
		score -= 10
	}

	// Bonus for executable magic bytes
	if isELF(content) || isMachO(content) || isPE(content) {
		score += 8
	}

	return score
}

func isELF(data []byte) bool {
	return len(data) > 4 && data[0] == 0x7f && data[1] == 'E' && data[2] == 'L' && data[3] == 'F'
}

func isMachO(data []byte) bool {
	if len(data) < 8 {
		return false
	}
	// Mach-O 32-bit (MH_MAGIC / MH_CIGAM)
	if data[0] == 0xfe && data[1] == 0xed && data[2] == 0xfa && data[3] == 0xce {
		return true
	}
	if data[0] == 0xce && data[1] == 0xfa && data[2] == 0xed && data[3] == 0xfe {
		return true
	}
	// Mach-O 64-bit (MH_MAGIC_64 / MH_CIGAM_64)
	if data[0] == 0xfe && data[1] == 0xed && data[2] == 0xfa && data[3] == 0xcf {
		return true
	}
	if data[0] == 0xcf && data[1] == 0xfa && data[2] == 0xed && data[3] == 0xfe {
		return true
	}
	return false
}

func isPE(data []byte) bool {
	return len(data) > 2 && data[0] == 'M' && data[1] == 'Z'
}

func isGzip(data []byte) bool {
	return len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b
}

func isZip(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K' && data[2] == 0x03 && data[3] == 0x04
}

// ComputeSHA256 returns hex-encoded sha256 checksum of data.
func ComputeSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// StartBackgroundCheck initiates recurring background update check and executes auto-update when enabled.
func StartBackgroundCheck(ctx context.Context, initialAutoUpdate bool) {
	SetAutoUpdate(initialAutoUpdate)

	// Check custom interval from env
	interval := DefaultCheckInterval
	if envHours := os.Getenv("AUTO_UPDATE_INTERVAL_HOURS"); envHours != "" {
		if h, err := strconv.Atoi(envHours); err == nil && h > 0 {
			interval = time.Duration(h) * time.Hour
		}
	}

	go func() {
		// Initial startup check after 5 seconds
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}

		runCheckCycle(ctx)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runCheckCycle(ctx)
			}
		}
	}()
}

func runCheckCycle(ctx context.Context) {
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	info, err := CheckUpdate(checkCtx)
	if err != nil {
		log.Debug("updater", "periodic update check failed", "error", err)
		return
	}

	if !info.HasUpdate {
		log.Debug("updater", "9router-go is up to date", "version", info.CurrentVersion)
		return
	}

	log.Info("updater", "NEW 9ROUTER-GO VERSION AVAILABLE!",
		"current", info.CurrentVersion,
		"latest", info.LatestVersion,
		"downloadUrl", info.DownloadURL,
	)

	// An unattended install must not move a user on a final release onto a
	// prerelease of the next one. The CLI and the dashboard trigger keep
	// working, so an RC is still one command away.
	if !autoApplyAllowed(info.CurrentVersion, info.LatestVersion) {
		log.Info("updater", "auto-update skipped: prerelease does not replace a final release — install it manually",
			"current", info.CurrentVersion, "latest", info.LatestVersion)
		return
	}

	if IsAutoUpdateEnabled() || os.Getenv("AUTO_UPDATE") == "true" {
		log.Info("updater", "auto-update is enabled — applying update...", "version", info.LatestVersion)
		if err := PerformSelfUpdate(info.DownloadURL, info.SHA256); err != nil {
			log.Error("updater", "auto-update download/apply failed", "error", err)
		} else {
			log.Info("updater", "auto-update applied successfully! Restarting process...")
			restartSelf()
		}
	}
}

// RestartSelf replaces this process with a fresh one running the updated
// binary.
//
// The replacement is spawned from the after-stop hook rather than here: this
// process still holds the listening socket, and a child that starts a moment
// earlier races it for the port. main runs the hook once fxApp.Stop has closed
// the listener, so the new process always finds the port free.
func RestartSelf() {
	execPath, err := os.Executable()
	if err != nil {
		log.Error("updater", "locate binary for restart failed", "error", err)
		return
	}

	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		log.Error("updater", "resolve symlink for restart failed", "error", err)
		return
	}

	spawn := func() {
		cmd := exec.Command(execPath, restartArgs()...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		cmd.Env = os.Environ()

		if err := cmd.Start(); err != nil {
			log.Error("updater", "spawn updated process failed", "error", err)
			os.Exit(1)
		}

		log.Info("updater", "replacement process started, old instance exiting", "pid", cmd.Process.Pid)
		// Release the handle so the child is never killed with this process
		// (Windows job control) and does not stay a zombie on POSIX.
		_ = cmd.Process.Release()
		os.Exit(0)
	}

	shutdown.RestartAfterStop(spawn)
}

// restartSelf is the indirection the cycle restarts through; a test swaps it so
// an applied update cannot take the test process down with it.
var restartSelf = RestartSelf

// restartArgs drops the background flag: the replacement process is the
// daemon already, and re-honouring the flag would make it spawn another one.
func restartArgs() []string {
	out := make([]string, 0, len(os.Args))
	for _, a := range os.Args[1:] {
		if a == daemon.BackgroundFlag || a == daemon.BackgroundAlias ||
			strings.HasPrefix(a, daemon.BackgroundFlag+"=") ||
			strings.HasPrefix(a, daemon.BackgroundAlias+"=") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// CompareVersions compares two semver strings (v1 > v2 -> 1, v1 < v2 -> -1, v1 == v2 -> 0).
func CompareVersions(v1, v2 string) int {
	return compareSemver(parseSemver(v1), parseSemver(v2))
}

// autoApplyAllowed decides whether the background cycle may install latest over
// current without a human in the loop. A prerelease never replaces a final
// release unattended: 1.9.7 must not silently become 1.9.8-rc1. Someone already
// running a prerelease is opted in, so prereleases keep rolling forward there,
// and a final release is always welcome.
func autoApplyAllowed(current, latest string) bool {
	return parseSemver(current).hasPrerelease() || !parseSemver(latest).hasPrerelease()
}
