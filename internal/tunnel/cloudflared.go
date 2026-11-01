package tunnel

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/config"
	"9router/proxy/internal/log"
)

// The upstream Node implementation downloads cloudflared into
// `<DATA_DIR>/bin/cloudflared`. This binary resolves the same data dir, so the
// path is shared on purpose: an operator who ran upstream 9router and then
// switched here must not download a second 30 MB binary, and a binary the
// operator placed there by hand must be used rather than replaced.
const (
	binDirName  = "bin"
	binBaseName = "cloudflared"

	// minBinarySize is the floor for a plausible cloudflared. A truncated
	// download or an HTML error page saved as the binary both fail here.
	minBinarySize = 1024 * 1024

	downloadTimeout = 10 * time.Minute
)

// releaseBaseURL is where the official binaries live. It is a variable so a
// test can point at a local file server instead of GitHub.
var releaseBaseURL = "https://github.com/cloudflare/cloudflared/releases/latest/download"

// Seams so tests never download a real binary or spawn a real process.
var (
	binLookPathFn = exec.LookPath
	spawnFn       = spawnCloudflared
	killByPortFn  = killCloudflaredByPort
)

// binaryName is the platform-specific file name: cloudflared.exe on Windows,
// cloudflared everywhere else.
func binaryName() string {
	if runtime.GOOS == "windows" {
		return binBaseName + ".exe"
	}
	return binBaseName
}

// BinPath is where this binary keeps its own copy of cloudflared.
//
// The path is derived from the data dir directly rather than from tunnelDir,
// so isolating one in a test does not silently relocate the other: a test that
// pins the tunnel state must not end up asserting against a bin directory that
// moved with it.
func BinPath() string {
	return filepath.Join(binDir(), binaryName())
}

// binDir is the directory holding the downloaded cloudflared. It sits beside
// the tunnel state under the same data dir, which is what lets the upstream
// Node implementation and this binary share one download.
func binDir() string {
	return filepath.Join(config.ResolveDataDir(), binDirName)
}

// assetName maps the running platform to the release asset. The Windows arm64
// case deliberately falls back to the 386 build, matching upstream: Cloudflare
// publishes no windows-arm64 asset, and the 386 binary runs on arm64 Windows
// through the OS's x86 emulation layer.
func assetName() (string, error) {
	goos, goarch := runtime.GOOS, runtime.GOARCH
	switch goos {
	case "windows":
		switch goarch {
		case "amd64":
			return "cloudflared-windows-amd64.exe", nil
		case "386":
			return "cloudflared-windows-386.exe", nil
		case "arm64":
			return "cloudflared-windows-386.exe", nil
		}
	case "darwin":
		switch goarch {
		case "amd64":
			return "cloudflared-darwin-amd64.tgz", nil
		case "arm64":
			return "cloudflared-darwin-arm64.tgz", nil
		}
	case "linux":
		switch goarch {
		case "amd64":
			return "cloudflared-linux-amd64", nil
		case "arm64":
			return "cloudflared-linux-arm64", nil
		case "386":
			return "cloudflared-linux-386", nil
		}
	}
	return "", fmt.Errorf("tunnel.assetName: unsupported platform %s/%s", goos, goarch)
}

// magicFor returns the leading bytes that identify a valid executable for the
// running platform: MZ for PE, the Mach-O magic pair for darwin, ELF for
// linux. A file that is present but starts with anything else is a failed
// download that was renamed, and must not be executed.
func magicFor() [][]byte {
	switch runtime.GOOS {
	case "windows":
		return [][]byte{{'M', 'Z'}}
	case "darwin":
		// cf fa ed fe (64-bit) and ce fa ed fe (little-endian 32-bit).
		return [][]byte{{0xcf, 0xfa, 0xed, 0xfe}, {0xce, 0xfa, 0xed, 0xfe}}
	default:
		return [][]byte{{0x7f, 'E', 'L', 'F'}}
	}
}

// isValidBinary reports whether path holds a complete, executable file for
// this platform. Size alone is not enough: a truncated download is large, and
// an HTML error page can be too.
func isValidBinary(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() < minBinarySize {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	// Each magic is a prefix, not a fixed-width field: PE is two bytes (MZ)
	// while Mach-O and ELF are four. Comparing the whole 4-byte read against a
	// 2-byte magic would reject every valid Windows binary.
	for _, magic := range magicFor() {
		if string(head[:len(magic)]) == string(magic) {
			return true
		}
	}
	return false
}

// FindCloudflared resolves the binary to run, preferring one the operator
// already has. PATH first, then the shared data-dir copy. A binary found on
// PATH is trusted as-is: it is the operator's own install, and re-downloading
// over it would discard a version they chose.
func FindCloudflared() string {
	if p, err := binLookPathFn(binBaseName); err == nil && p != "" {
		return p
	}
	if p := BinPath(); isValidBinary(p) {
		return p
	}
	return ""
}

// downloadMu serialises downloads so two concurrent enables cannot write the
// same temp file. A singleflight would also work; a mutex is the boring option
// and the download is not on a hot path.
var downloadMu sync.Mutex

// EnsureCloudflared returns a usable binary path, downloading one when needed.
// It is safe to call concurrently and is idempotent once the binary exists.
func EnsureCloudflared(ctx context.Context) (string, error) {
	if p := FindCloudflared(); p != "" {
		log.Debug("tunnel", "using existing cloudflared", "path", p)
		return p, nil
	}

	downloadMu.Lock()
	defer downloadMu.Unlock()

	// Re-check under the lock: another caller may have finished the download
	// while this one waited.
	if p := FindCloudflared(); p != "" {
		log.Debug("tunnel", "using existing cloudflared", "path", p)
		return p, nil
	}

	if err := os.MkdirAll(binDir(), 0o755); err != nil {
		return "", fmt.Errorf("tunnel.EnsureCloudflared: create bin dir: %w", err)
	}

	asset, err := assetName()
	if err != nil {
		return "", err
	}
	url := releaseBaseURL + "/" + asset
	log.Info("tunnel", "downloading cloudflared", "url", url, "platform", runtime.GOOS+"/"+runtime.GOARCH)

	if strings.HasSuffix(asset, ".tgz") {
		p, err := downloadAndExtract(ctx, url)
		if err != nil {
			log.Error("tunnel", "cloudflared download failed", "url", url, "error", err)
			return "", err
		}
		log.Info("tunnel", "cloudflared installed", "path", p)
		return p, nil
	}
	p, err := downloadBinary(ctx, url, BinPath())
	if err != nil {
		log.Error("tunnel", "cloudflared download failed", "url", url, "error", err)
		return "", err
	}
	log.Info("tunnel", "cloudflared installed", "path", p)
	return p, nil
}

// downloadBinary streams the asset to a temp file next to its destination and
// renames it into place. Writing beside the target keeps the rename on one
// filesystem, so it is atomic rather than a copy that can be interrupted
// half-written.
func downloadBinary(ctx context.Context, url, dest string) (string, error) {
	tmp, err := os.CreateTemp(binDir(), ".cloudflared-*.tmp")
	if err != nil {
		return "", fmt.Errorf("tunnel.downloadBinary: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds

	if err := fetchToFile(ctx, url, tmp); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("tunnel.downloadBinary: close temp: %w", err)
	}
	if !isValidBinary(tmpName) {
		return "", fmt.Errorf("tunnel.downloadBinary: %s is not a valid %s executable", url, runtime.GOOS)
	}
	// 0755 before the rename, so the binary is never briefly present without
	// its execute bit.
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return "", fmt.Errorf("tunnel.downloadBinary: chmod: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return "", fmt.Errorf("tunnel.downloadBinary: install: %w", err)
	}
	return dest, nil
}

// downloadAndExtract handles the macOS .tgz release, which wraps the binary in
// a tarball. The archive is streamed rather than buffered, and only the
// cloudflared member is written out.
func downloadAndExtract(ctx context.Context, url string) (string, error) {
	tmp, err := os.CreateTemp(binDir(), ".cloudflared-*.tgz.tmp")
	if err != nil {
		return "", fmt.Errorf("tunnel.downloadAndExtract: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := fetchToFile(ctx, url, tmp); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("tunnel.downloadAndExtract: close temp: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("tunnel.downloadAndExtract: rewind: %w", err)
	}

	dest := BinPath()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", fmt.Errorf("tunnel.downloadAndExtract: create binary: %w", err)
	}

	gz, err := gzip.NewReader(tmp)
	if err != nil {
		_ = out.Close()
		_ = os.Remove(dest)
		return "", fmt.Errorf("tunnel.downloadAndExtract: gunzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	found := false
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = out.Close()
			_ = os.Remove(dest)
			return "", fmt.Errorf("tunnel.downloadAndExtract: untar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != binBaseName {
			continue
		}
		if _, err := io.Copy(out, tr); err != nil {
			_ = out.Close()
			_ = os.Remove(dest)
			return "", fmt.Errorf("tunnel.downloadAndExtract: extract: %w", err)
		}
		found = true
		break
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dest)
		return "", fmt.Errorf("tunnel.downloadAndExtract: close binary: %w", err)
	}
	if !found {
		_ = os.Remove(dest)
		return "", fmt.Errorf("tunnel.downloadAndExtract: no %s member in %s", binBaseName, url)
	}
	if !isValidBinary(dest) {
		_ = os.Remove(dest)
		return "", fmt.Errorf("tunnel.downloadAndExtract: extracted binary is not valid for %s", runtime.GOOS)
	}
	return dest, nil
}

// fetchToFile streams url into w, following redirects (the GitHub release URL
// redirects to the CDN) and reporting the byte count on failure so a truncated
// transfer is diagnosable.
func fetchToFile(ctx context.Context, url string, w io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("tunnel.fetchToFile: request: %w", err)
	}
	client := &http.Client{
		// The release URL redirects to objects.githubusercontent.com; the
		// default policy already follows it, but the timeout must cover the
		// whole chain, hence the shared context.
		Timeout: downloadTimeout,
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("tunnel.fetchToFile: %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tunnel.fetchToFile: %s returned %s", url, resp.Status)
	}

	n, err := io.Copy(w, resp.Body)
	if err != nil {
		return fmt.Errorf("tunnel.fetchToFile: %s after %d bytes: %w", url, n, err)
	}
	if n < minBinarySize {
		return fmt.Errorf("tunnel.fetchToFile: %s returned only %d bytes", url, n)
	}
	return nil
}

// SpawnOptions describes one cloudflared invocation.
type SpawnOptions struct {
	// QuickTunnel runs `tunnel --url <origin>` with no account. NamedTunnel
	// runs `tunnel run --token <token>` against a pre-created tunnel.
	QuickTunnel bool
	Token       string
	Origin      string // http://127.0.0.1:<port> for quick tunnels
	Protocol    string // http2 | quic | auto
	ConfigPath  string // quick-tunnel config placeholder
	OnURL       func(url string)
}

// SpawnResult reports the child and, for a quick tunnel, the URL it was
// assigned. A named tunnel has no URL to discover: the operator's DNS already
// points at it.
type SpawnResult struct {
	Cmd       *exec.Cmd
	TunnelURL string
}

// spawnCloudflared starts cloudflared detached from the caller's request.
//
// Detachment is the whole point: the HTTP request that enabled the tunnel
// finishes, the browser tab closes, the dashboard restarts — the tunnel must
// survive all three. exec.Command (not CommandContext) is what makes that
// possible, and it is why the PID file exists.
func spawnCloudflared(bin string, opts SpawnOptions) (*SpawnResult, error) {
	// Start from a clean buffer so a mismatch detected later belongs to this
	// connector, not to the one a previous enable left behind.
	outputTail.reset()

	args := buildArgs(opts)
	//nolint:noctx // deliberate: the tunnel must outlive the request that
	// created it. Binding it to a request context would kill cloudflared the
	// moment the operator closed the dashboard tab.
	cmd := exec.Command(bin, args...) //nolint:gosec // bin resolved from PATH or the data dir
	cmd.Dir = os.TempDir()
	configureDetached(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("tunnel.spawnCloudflared: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("tunnel.spawnCloudflared: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		log.Error("tunnel", "cloudflared failed to start", "bin", bin, "error", err)
		return nil, fmt.Errorf("tunnel.spawnCloudflared: start: %w", err)
	}
	log.Info("tunnel", "cloudflared started", "pid", cmd.Process.Pid, "args", strings.Join(args, " "))
	if err := SavePID(cmd.Process.Pid); err != nil {
		// A tunnel that is up but unrecorded cannot be found again, so the
		// spawn is treated as failed rather than left to leak.
		killProcess(cmd.Process.Pid)
		return nil, fmt.Errorf("tunnel.spawnCloudflared: record pid: %w", err)
	}

	res := &SpawnResult{Cmd: cmd}

	if opts.QuickTunnel {
		url, err := awaitQuickTunnelURL(cmd, stdout, stderr, opts.OnURL)
		if err != nil {
			killProcess(cmd.Process.Pid)
			_ = ClearPID(cmd.Process.Pid)
			return nil, err
		}
		res.TunnelURL = url
	} else {
		// A named tunnel has no URL to discover in its output, but its output
		// is still the only record of why a connector failed. Discarding it —
		// as this branch once did — left a bad token, an unrouted hostname and
		// a blocked port all producing the same silence, and the operator got
		// a health-check timeout with nothing to act on.
		//
		// The output is forwarded to the console log rather than parsed: a
		// named tunnel's readiness is the operator's hostname answering, which
		// the caller's health probe confirms.
		go drainToLog(io.TeeReader(stdout, outputTail))
		go drainToLog(io.TeeReader(stderr, outputTail))
	}

	// Reap the child so it does not linger as a zombie once it exits.
	go func() { _ = cmd.Wait() }()
	return res, nil
}

// outputTail keeps the most recent cloudflared output for the whole package.
//
// It is shared rather than per-spawn because the failure that needs diagnosing
// happens after the spawn has returned: the health check is waiting on a
// hostname, and the only record of why the origin is unreachable is in the
// connector's output. A per-spawn buffer would be out of scope by then.
var outputTail = &tailBuffer{limit: 32 * 1024}

// tailBuffer is a bounded, concurrency-safe record of recent output.
type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

// snapshot returns a copy of the retained output. A copy so the caller reads a
// stable view while the connector keeps appending.
func (t *tailBuffer) snapshot() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func (t *tailBuffer) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = nil
}

// originMismatch describes a connector whose ingress rule points somewhere this
// gateway is not listening. It is the failure a health-check timeout cannot
// distinguish from a dead tunnel, and it is common enough to name explicitly:
// a tunnel created in the Cloudflare dashboard keeps whatever origin it was
// given, which is usually the port of whatever the operator ran before.
type originMismatch struct {
	// Configured is the origin the connector reports it is dialing.
	Configured string
	// Expected is the port this gateway actually listens on.
	Expected int
}

// detectOriginMismatch reads the connector's recent output and reports an
// origin that does not match this gateway's port.
//
// The signal is the pair cloudflared prints when it cannot reach the origin:
// `originService=http://localhost:20128` alongside a refused dial. Matching on
// the origin service alone is enough, because a connector that is reaching its
// origin does not print it.
func detectOriginMismatch(output string, expectedPort int) (originMismatch, bool) {
	if output == "" || expectedPort <= 0 {
		return originMismatch{}, false
	}
	for line := range strings.SplitSeq(output, "\n") {
		idx := strings.Index(line, "originService=")
		if idx < 0 {
			continue
		}
		field := line[idx+len("originService="):]
		// The field runs to the next space, or to the end of the line.
		if end := strings.IndexAny(field, " \t\r"); end >= 0 {
			field = field[:end]
		}
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		u, err := neturl.Parse(field)
		if err != nil {
			continue
		}
		port := u.Port()
		if port == "" {
			// An origin with no port is 80 or 443, neither of which is this
			// gateway's default, so it is still a mismatch worth reporting.
			continue
		}
		if port != strconv.Itoa(expectedPort) {
			return originMismatch{Configured: field, Expected: expectedPort}, true
		}
	}
	return originMismatch{}, false
}

// drainToLog forwards a cloudflared pipe to the console log, line by line.
//
// It is a drain as well as a logger: a pipe nobody reads fills its buffer and
// blocks the child, which would stall a named tunnel with no error to show for
// it. Only lines that carry a recognisable severity are kept, so a chatty
// connector does not flood the console with its periodic stats.
func drainToLog(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		switch {
		case strings.Contains(line, "ERR"), strings.Contains(line, "error"):
			log.Error("cloudflared", line)
		case strings.Contains(line, "WRN"), strings.Contains(line, "failed"):
			log.Warn("cloudflared", line)
		default:
			log.Debug("cloudflared", line)
		}
	}
}

// extractConnectorToken pulls a tunnel token out of whatever the operator
// pasted.
//
// The Cloudflare dashboard's install command is what people copy, because it is
// what the page puts in front of them:
//
//	cloudflared.exe service install eyJhIjoi...
//
// Pasted whole, that arrives as one string and `--token` receives all of it,
// which cloudflared rejects as a malformed token. The token itself is a
// base64url JWT — three dot-separated segments starting "eyJ" — so it is
// recognisable wherever it appears in the input, and the rest is discarded.
func extractConnectorToken(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Already a bare token: nothing to strip.
	if !strings.ContainsAny(raw, " \t\r\n") {
		return raw
	}
	for _, field := range strings.Fields(raw) {
		if isConnectorToken(field) {
			return field
		}
	}
	// No recognisable token in the input. Returning it unchanged lets
	// cloudflared reject it with its own message, which names the real problem
	// better than a guess would.
	return raw
}

// isConnectorToken reports whether s looks like a Cloudflare connector token.
//
// The token is a base64url blob, and it is not always a JWT: the tokens the
// dashboard issues for tunnels created through the API carry no dots at all,
// while the ones from a manually created tunnel do. Requiring the three-segment
// JWT shape would reject the former, so the check is on the alphabet and the
// length instead — both of which every connector token satisfies and no
// ordinary command word does.
func isConnectorToken(s string) bool {
	// A real token is long; "install", "service" and "run" are not.
	if len(s) < 40 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// buildArgs assembles the cloudflared command line.
func buildArgs(opts SpawnOptions) []string {
	if !opts.QuickTunnel {
		return []string{
			"tunnel", "run",
			"--dns-resolver-addrs", "1.1.1.1:53",
			"--token", extractConnectorToken(opts.Token),
		}
	}
	args := []string{
		"tunnel",
		"--url", opts.Origin,
		"--no-autoupdate",
		"--retries", "99",
	}
	if opts.ConfigPath != "" {
		args = append(args, "--config", opts.ConfigPath)
	}
	if opts.Protocol != "" {
		args = append(args, "--protocol", opts.Protocol)
	}
	return args
}

// quickTunnelTimeout bounds how long cloudflared gets to print its URL. The
// upstream Node implementation waits 90s; the same budget applies here.
const quickTunnelTimeout = 90 * time.Second

// awaitQuickTunnelURL reads cloudflared's output until the assigned
// trycloudflare.com hostname appears.
//
// Both pipes are read in their own goroutine. cloudflared writes its banner
// and diagnostics to stderr and the URL line to stdout, and a pipe nobody
// drains fills its buffer and blocks the child — which would deadlock the
// spawn with no error to show for it.
func awaitQuickTunnelURL(cmd *exec.Cmd, stdout, stderr io.Reader, onURL func(string)) (string, error) {
	urlCh := make(chan string, 1)

	// logTail keeps the last of cloudflared's output so a timeout can report
	// what the process actually said instead of an empty string.
	var mu sync.Mutex
	var logTail string
	keep := func(chunk string) {
		mu.Lock()
		defer mu.Unlock()
		logTail = (logTail + chunk)
		if len(logTail) > 4000 {
			logTail = logTail[len(logTail)-4000:]
		}
	}
	tail := func() string {
		mu.Lock()
		defer mu.Unlock()
		if logTail == "" {
			return "(no output)"
		}
		return logTail
	}

	scan := func(r io.Reader) {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				keep(chunk)
				if url := extractQuickTunnelURL(chunk); url != "" {
					select {
					case urlCh <- url:
					default:
					}
				}
			}
			if err != nil {
				return
			}
		}
	}
	go scan(stdout)
	go scan(stderr)

	var firstURL string
	timer := time.NewTimer(quickTunnelTimeout)
	defer timer.Stop()

	for {
		select {
		case url := <-urlCh:
			if firstURL == "" {
				firstURL = url
				log.Info("tunnel", "quick tunnel URL assigned", "url", url)
				// The first URL resolves this call; later ones arrive as
				// reconnect notifications and must not block the return.
				go watchURLChange(urlCh, onURL)
				return firstURL, nil
			}
		case <-timer.C:
			killProcess(cmd.Process.Pid)
			log.Error("tunnel", "no tunnel URL assigned within budget", "budgetSec", int(quickTunnelTimeout.Seconds()), "lastOutput", tail())
			return "", fmt.Errorf("tunnel.awaitQuickTunnelURL: no tunnel URL within %ds. Last output: %s", quickTunnelTimeout/time.Second, tail())
		case <-time.After(50 * time.Millisecond):
			if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
				log.Error("tunnel", "cloudflared exited before assigning a URL", "lastOutput", tail())
				return "", fmt.Errorf("tunnel.awaitQuickTunnelURL: cloudflared exited before assigning a URL. Last output: %s", tail())
			}
		}
	}
}

// watchURLChange forwards a rotated quick-tunnel URL to the caller. A quick
// tunnel's hostname changes when cloudflared reconnects to a different edge,
// and the registered URL must follow it or the public URL goes stale.
func watchURLChange(urlCh <-chan string, onURL func(string)) {
	for url := range urlCh {
		if onURL != nil {
			onURL(url)
		}
	}
}

// extractQuickTunnelURL finds the tunnel hostname in a chunk of cloudflared
// output.
//
// The output also names api.trycloudflare.com, which is the control-plane host
// and not the tunnel endpoint. Matching it would register a URL that answers
// nothing. The last non-api match wins, because cloudflared prints the
// candidate URL before it confirms the connection.
func extractQuickTunnelURL(s string) string {
	var last string
	rest := s
	for {
		idx := strings.Index(rest, "https://")
		if idx < 0 {
			break
		}
		rest = rest[idx+len("https://"):]
		end := strings.IndexAny(rest, " \t\r\n\"'`,;)")
		host := rest
		if end >= 0 {
			host = rest[:end]
		}
		if strings.HasSuffix(host, ".trycloudflare.com") && host != "api.trycloudflare.com" {
			last = "https://" + host
		}
	}
	return last
}

// Kill terminates the recorded cloudflared and any other instance bound to
// port. The port sweep matters because a tunnel started by the upstream Node
// binary, or by an earlier run of this one that lost its PID file, would
// otherwise keep holding the port and the origin.
func Kill(port int) {
	pid := LoadPID()
	if pid > 0 {
		killProcess(pid)
		_ = ClearPID(pid)
	}
	if killByPortFn != nil {
		killByPortFn(port)
	}
}

// IsRunning reports whether the recorded cloudflared is still alive. A PID file
// with no live process means the tunnel died without cleaning up, which is the
// case the watchdog exists to repair.
func IsRunning() bool {
	return processAlive(LoadPID())
}

// killProcess terminates one PID. It is best-effort by design: the caller is
// usually a disable or a restart, and neither should fail because the process
// had already exited.
func killProcess(pid int) {
	if pid <= 0 {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = proc.Kill()
}
