package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"9router/proxy/internal/providers"
)

// Live Claude Code CLI version for the claude-cli/x.y.z fingerprint — the Go
// port of open-sse/utils/claudeCliVersion.js (upstream 43bc940e). Anthropic
// gates new models on a minimum CLI version (error_code
// "claude_code_version_too_old"), so a hardcoded version goes stale with every
// model launch. The latest published @anthropic-ai/claude-code is resolved
// from npm, cached, and never applied below the bundled fallback.
//
// Precedence, matching upstream getClaudeCliVersion: the CLAUDE_CODE_VERSION
// env pin (also disables the lookup), then a version adopted from an upstream
// rejection, then the npm lookup when it is strictly newer than the fallback,
// and the fallback itself otherwise.

const (
	claudeCLINPMURL     = "https://registry.npmjs.org/@anthropic-ai/claude-code/latest"
	claudeCLIVersionTTL = 6 * time.Hour
	// A failed lookup is retried far sooner than a successful one so a blip
	// does not pin a stale fingerprint for six hours (upstream RETRY_MS).
	claudeCLIVersionRetry = 10 * time.Minute
	claudeCLIVersionTO    = 3 * time.Second
)

var claudeCLIVersionRE = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// claudeCLIVersionState is the module-level cache. nextCheckAt mirrors
// upstream's nextCheckAt; inflight collapses concurrent cold reads.
var (
	claudeCLIVersionMu      sync.RWMutex
	claudeCLIVersionCurrent string
	claudeCLINextCheckAt    time.Time
	claudeCLIVersionFlight  singleflight.Group
)

// ClaudeCLIVersion returns the active Claude Code CLI version. An env override
// CLAUDE_CODE_VERSION wins; otherwise any adopted version from a
// claude_code_version_too_old upstream rejection; otherwise the newer of the
// npm-resolved version and defaultClaudeCLIVersion.
// Mirrors OMP getClaudeCodeVersion() in claude-code-fingerprint.ts.
func ClaudeCLIVersion() string {
	if env := os.Getenv("CLAUDE_CODE_VERSION"); env != "" {
		return env
	}
	claudeVersionMu.RLock()
	adopted := adoptedClaudeVersion
	claudeVersionMu.RUnlock()
	claudeCLIVersionMu.RLock()
	live := claudeCLIVersionCurrent
	claudeCLIVersionMu.RUnlock()
	if adopted != "" {
		return adopted
	}
	if claudeCLIVersionCmp(live, defaultClaudeCLIVersion) > 0 {
		return live
	}
	return defaultClaudeCLIVersion
}

// ClaudeCLIVersionLookupTTL reports whether the cached npm answer is still
// fresh. Exported for tests that drive time.
func claudeCLIVersionFresh() bool {
	claudeCLIVersionMu.RLock()
	defer claudeCLIVersionMu.RUnlock()
	return time.Now().Before(claudeCLINextCheckAt)
}

// RefreshClaudeCLIVersion resolves the npm version when the cache is stale.
// It awaits at most claudeCLIVersionTO; failures keep the last known value and
// schedule a retry. Upstream ensureClaudeCliVersion. Safe to call on every
// request: past the deadline one shared flight does the network call and the
// rest await its answer.
func RefreshClaudeCLIVersion(ctx context.Context) {
	if os.Getenv("CLAUDE_CODE_VERSION") != "" || claudeCLIVersionFresh() {
		return
	}
	_, _, _ = claudeCLIVersionFlight.Do("lookup", func() (any, error) {
		return nil, lookupClaudeCLIVersion(ctx)
	})
}

func lookupClaudeCLIVersion(ctx context.Context) error {
	reqCtx, cancel := context.WithTimeout(ctx, claudeCLIVersionTO)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, claudeCLINPMURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		scheduleClaudeCLIVersionRetry()
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		scheduleClaudeCLIVersionRetry()
		return fmt.Errorf("npm %d", resp.StatusCode)
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || !claudeCLIVersionRE.MatchString(body.Version) {
		scheduleClaudeCLIVersionRetry()
		if err == nil {
			err = fmt.Errorf("bad version")
		}
		return err
	}
	claudeCLIVersionMu.Lock()
	claudeCLIVersionCurrent = body.Version
	claudeCLINextCheckAt = time.Now().Add(claudeCLIVersionTTL)
	claudeCLIVersionMu.Unlock()
	return nil
}

func scheduleClaudeCLIVersionRetry() {
	claudeCLIVersionMu.Lock()
	claudeCLINextCheckAt = time.Now().Add(claudeCLIVersionRetry)
	claudeCLIVersionMu.Unlock()
}

// claudeCLIVersionCmp compares dotted triples numerically; a malformed or
// empty value sorts before everything (mirrors upstream cmp + the
// "never downgrade to an invalid version" guard).
func claudeCLIVersionCmp(a, b string) int {
	if !claudeCLIVersionRE.MatchString(a) {
		return -1
	}
	if !claudeCLIVersionRE.MatchString(b) {
		return 1
	}
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	for i := range 3 {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x - y
		}
	}
	return 0
}

// ApplyClaudeCLIVersionHeader rewrites claude-cli/x.y.z in a header value to
// the live version (upstream applyClaudeCliVersion). A fingerprint older than
// 1.0.0 is left alone — never downgrade to a placeholder.
func ApplyClaudeCLIVersionHeader(value string) string {
	v := ClaudeCLIVersion()
	if !claudeCLIVersionRE.MatchString(v) || claudeCLIVersionCmp(v, "1.0.0") < 0 {
		return value
	}
	return claudeCLIUserAgentRE.ReplaceAllString(value, "claude-cli/"+v)
}

var claudeCLIUserAgentRE = regexp.MustCompile(`claude-cli/\d+\.\d+\.\d+`)

// withClaudeIdentity stamps the live claude-cli UA onto the claude provider's
// static headers. The registry literal cannot carry it (the version moves with
// the npm feed), so it is applied here per request on the already-cloned
// config — the same shape withClineIdentity uses. Never overrides an operator
// Custom Header: ApplyHeaderOverrides wins later anyway, and a stored override
// merged into StaticHeaders by applyProviderOverrides is treated as an
// operator value and left untouched.
func (h *ChatHandler) withClaudeIdentity(provider string, cfg *providers.ProviderConfig) *providers.ProviderConfig {
	if cfg == nil || provider != "claude" {
		return cfg
	}
	RefreshClaudeCLIVersion(context.Background())
	if _, ok := cfg.StaticHeaders["User-Agent"]; !ok {
		return cfg
	}
	cloned := *cfg
	cloned.StaticHeaders = make(map[string]string, len(cfg.StaticHeaders))
	for k, v := range cfg.StaticHeaders {
		cloned.StaticHeaders[k] = v
	}
	cloned.StaticHeaders["User-Agent"] = ApplyClaudeCLIVersionHeader(cloned.StaticHeaders["User-Agent"])
	return &cloned
}
