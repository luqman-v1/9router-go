package headroom

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Compression extras tracked by the dashboard. `proxy` is the base install;
// `code` adds tree-sitter AST compression, `ml` adds the Kompress-v2 HF model.
var CompressionExtras = []string{"code", "ml"}

// ExtraMarkers are the pip packages that back each compression extra.
var ExtraMarkers = map[string][]string{
	"code": {"tree-sitter", "tree-sitter-language-pack"},
	"ml":   {"torch", "huggingface-hub"},
}

const (
	// DefaultURL is the fallback Headroom proxy URL.
	DefaultURL = "http://localhost:8787"
	// DefaultPort is used when the configured URL has no explicit port.
	DefaultPort = 8787
)

const (
	pipTimeout    = 8 * time.Second
	healthTimeout = 1500 * time.Millisecond
)

var (
	pythonCandNames = []string{"python3.13", "python3.12", "python3.11", "python3.10", "python3", "python"}
	// loopbackHosts is the detect.js LOOPBACK_HOSTS set (Go's URL.Hostname()
	// already strips IPv6 brackets, so "[::1]" maps to "::1" here).
	loopbackHosts = map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true, "0.0.0.0": true}
	versionRe     = regexp.MustCompile(`(\d+)\.(\d+)`)

	healthClient = &http.Client{Timeout: healthTimeout}
)

func whichCommand() string {
	if runtime.GOOS == "windows" {
		return "where"
	}
	return "which"
}

// extraBinDirs lists known Python/headroom bin dirs often missing from a
// packaged/launchd PATH (port of detect.js EXTRA_BINS, unix branch).
func extraBinDirs() []string {
	home, _ := os.UserHomeDir()
	dirs := []string{
		"/usr/local/bin",
		"/opt/homebrew/bin",
		"/Library/Frameworks/Python.framework/Versions/3.13/bin",
		"/Library/Frameworks/Python.framework/Versions/3.12/bin",
		"/Library/Frameworks/Python.framework/Versions/3.11/bin",
		"/Library/Frameworks/Python.framework/Versions/3.10/bin",
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".local/bin"))
	}
	return append(dirs, "/usr/bin", "/bin")
}

// extendedPath builds the detection PATH: extra bin dirs first, then PATH.
func extendedPath() string {
	bins := extraBinDirs()
	if p := os.Getenv("PATH"); p != "" {
		bins = append(bins, p)
	}
	return strings.Join(bins, string(os.PathListSeparator))
}

// runOutput runs name with the extended PATH and returns trimmed stdout.
func runOutput(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "PATH="+extendedPath())
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

// FindHeadroomBinary locates the `headroom` CLI, first line of `which/where`.
func FindHeadroomBinary() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := runOutput(ctx, whichCommand(), "headroom")
	if err != nil || out == "" {
		return ""
	}
	return strings.Split(out, "\n")[0]
}

// pythonCandidates lists interpreters to probe: next to the headroom binary,
// then full paths from the extra bin dirs, then bare names via PATH.
func pythonCandidates() []string {
	var list []string
	if bin := FindHeadroomBinary(); bin != "" {
		dir := filepath.Dir(bin)
		for _, n := range []string{"python3", "python3.13", "python"} {
			list = append(list, filepath.Join(dir, n))
		}
	}
	for _, dir := range extraBinDirs() {
		for _, n := range pythonCandNames {
			list = append(list, filepath.Join(dir, n))
		}
	}
	list = append(list, pythonCandNames...)
	return list
}

// FindPython310 finds a Python >= 3.10, preferring the interpreter that can
// see the installed headroom-ai package.
func FindPython310() string {
	var fallback string
	for _, candidate := range pythonCandidates() {
		ctx, cancel := context.WithTimeout(context.Background(), pipTimeout)
		ver, err := runOutput(ctx, candidate, "--version")
		cancel()
		if err != nil {
			continue
		}
		m := versionRe.FindStringSubmatch(ver)
		if m == nil {
			continue
		}
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		if major < 3 || (major == 3 && minor < 10) {
			continue
		}
		if fallback == "" {
			fallback = candidate
		}
		ctx2, cancel2 := context.WithTimeout(context.Background(), pipTimeout)
		_, err = runOutput(ctx2, candidate, "-m", "pip", "show", "headroom-ai")
		cancel2()
		if err == nil {
			return candidate
		}
	}
	return fallback
}

// ProbeProxyRunning reports whether the Headroom proxy answers GET /health.
func ProbeProxyRunning(urlStr string) bool {
	if urlStr == "" {
		return false
	}
	base := strings.TrimRight(urlStr, "/")
	resp, err := healthClient.Get(base + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// IsLoopbackHeadroomUrl reports whether the URL points at a loopback host.
func IsLoopbackHeadroomUrl(rawurl string) bool {
	u, err := url.Parse(rawurl)
	if err != nil {
		return false
	}
	return loopbackHosts[strings.ToLower(u.Hostname())]
}

// Extras mirrors the marker-package status for each compression extra.
type Extras struct {
	Code bool `json:"code"`
	ML   bool `json:"ml"`
}

// ExtrasStatus is the parsed result of `pip list --format=json`.
type ExtrasStatus struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
	Extras    Extras `json:"extras"`
}

// ParsePipList parses `pip list --format=json` output into ExtrasStatus.
func ParsePipList(data []byte) ExtrasStatus {
	var pkgs []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkgs); err != nil {
		return ExtrasStatus{}
	}
	names := make(map[string]bool, len(pkgs))
	for _, p := range pkgs {
		names[strings.ToLower(p.Name)] = true
	}
	if !names["headroom-ai"] {
		return ExtrasStatus{}
	}
	st := ExtrasStatus{Installed: true}
	for _, p := range pkgs {
		if strings.ToLower(p.Name) == "headroom-ai" {
			st.Version = p.Version
			break
		}
	}
	for _, extra := range CompressionExtras {
		for _, marker := range ExtraMarkers[extra] {
			if names[strings.ToLower(marker)] {
				if extra == "code" {
					st.Extras.Code = true
				} else if extra == "ml" {
					st.Extras.ML = true
				}
				break
			}
		}
	}
	return st
}

// GetInstalledHeadroomExtras answers installed version + active extras from
// one `pip list` call. `python` may be "" to auto-detect.
func GetInstalledHeadroomExtras(python string) ExtrasStatus {
	if python == "" {
		python = FindPython310()
	}
	if python == "" {
		return ExtrasStatus{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), pipTimeout)
	defer cancel()
	out, err := runOutput(ctx, python, "-m", "pip", "list", "--format=json", "--disable-pip-version-check")
	if err != nil {
		return ExtrasStatus{}
	}
	return ParsePipList([]byte(out))
}

// GetHeadroomStatus aggregates the dashboard status object.
func GetHeadroomStatus(urlStr string) map[string]any {
	binary := FindHeadroomBinary()
	python := FindPython310()
	installed := binary != ""
	running := ProbeProxyRunning(urlStr)
	localURL := IsLoopbackHeadroomUrl(urlStr)
	var extras ExtrasStatus
	if installed {
		extras = GetInstalledHeadroomExtras(python)
	}
	return map[string]any{
		"installed": installed,
		"path":      strOrNil(binary),
		"running":   running,
		"python":    strOrNil(python),
		"localUrl":  localURL,
		"canStart":  installed && localURL,
		"version":   extras.Version,
		"extras":    extras.Extras,
	}
}

// ParsePortFromURL extracts the explicit port (0 when absent/invalid).
func ParsePortFromURL(urlStr string) int {
	u, err := url.Parse(urlStr)
	if err != nil {
		return 0
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		return 0
	}
	if p > 0 && p < 65536 {
		return p
	}
	return 0
}

var dashboardFetchRe = regexp.MustCompile(`fetch\('(/(?:stats|health|stats-history|transformations/feed))`)

// RewriteDashboardHTML rewrites headroom dashboard fetches to route back
// through the proxy (port of the Next proxy route's regex).
func RewriteDashboardHTML(html string) string {
	return dashboardFetchRe.ReplaceAllString(html, `fetch('/api/headroom/proxy$1`)
}

func strOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}
