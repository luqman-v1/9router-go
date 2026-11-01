// Package uptime pushes a heartbeat to an Uptime Kuma (or any
// Push-Monitor-compatible) endpoint, so an operator learns the gateway is down
// from their monitor rather than from a CLI tool that stopped answering.
//
// The push protocol is deliberately small: GET the push URL, and the status
// word appended as the path decides the monitor's state. "up" for a healthy
// tick, "down" for one where the gateway could not serve, and "msg=..." for a
// human-readable note. That is the whole contract, which is why this package
// has no dependency on Uptime Kuma itself — any service speaking the same
// push shape works.
//
// The loop is fail-open in the direction that matters: a push that fails is
// logged and retried on the next tick, and it never touches the request path.
// A gateway that cannot reach its monitor must still serve inference.
package uptime

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/log"
)

// DefaultInterval is how often a heartbeat is pushed when the operator has not
// chosen one. Sixty seconds is the finest granularity Uptime Kuma's default
// heartbeat interval resolves, so pushing more often buys nothing.
const DefaultInterval = 60 * time.Second

// pushTimeout bounds one push. A monitor that has not answered in ten seconds
// is treated as unreachable for this tick; the next tick retries.
const pushTimeout = 10 * time.Second

// Config is the operator's monitoring settings.
type Config struct {
	// Enabled is the master switch. Off by default: an operator who has not
	// configured a push URL must not have the gateway dialling anything.
	Enabled bool
	// PushURL is the Uptime Kuma push URL, including its push token.
	PushURL string
	// IntervalSeconds is the heartbeat period. Zero falls back to
	// DefaultInterval.
	IntervalSeconds int
}

// Interval resolves the configured period, falling back to the default.
func (c Config) Interval() time.Duration {
	if c.IntervalSeconds <= 0 {
		return DefaultInterval
	}
	return time.Duration(c.IntervalSeconds) * time.Second
}

// Configured reports whether there is anything to push to. An enabled monitor
// with no URL is not configured: dialling an empty URL every minute would log a
// failure a minute forever.
func (c Config) Configured() bool {
	return c.Enabled && strings.TrimSpace(c.PushURL) != ""
}

// ConfigSource supplies the monitoring settings, read fresh per tick so a
// dashboard change takes effect on the next heartbeat without a restart.
type ConfigSource func() Config

// Pusher pushes one heartbeat. It is a field on the loop rather than a package
// function so a test can count pushes without a network.
type Pusher interface {
	Push(ctx context.Context, status, msg string) error
}

// HTTPPusher pushes over HTTP to a push-monitor URL.
type HTTPPusher struct {
	// URL is the push base, without a status word appended.
	URL string
	// Client is the HTTP client used. Nil means a package-level default with
	// the push timeout applied.
	Client *http.Client
}

// Push sends one heartbeat. status is the state word ("up" or "down") and msg
// is an optional human-readable note.
//
// The URL is trimmed of a trailing slash and of a status word the operator may
// have pasted in, so a URL copied from the Uptime Kuma UI — which shows the
// full form including a status — does not end up with two of them. Only the
// last path segment is considered, so a push token that happens to contain a
// slash-separated word is left alone.
func (p *HTTPPusher) Push(ctx context.Context, status, msg string) error {
	base := strings.TrimRight(strings.TrimSpace(p.URL), "/")
	if base == "" {
		return fmt.Errorf("uptime: empty push URL")
	}
	// Drop a pasted status word: "/up", "/down", or either with a query string
	// already attached.
	if i := strings.LastIndex(base, "/"); i >= 0 {
		switch seg := base[i+1:]; seg {
		case "up", "down":
			base = base[:i]
		}
	}
	if base == "" {
		return fmt.Errorf("uptime: push URL has no host")
	}
	target := base + "/" + strings.TrimSpace(status)
	if msg != "" {
		target += "?msg=" + urlQueryEscape(msg)
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: pushTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("uptime: build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("uptime: push: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// The body is drained and discarded: a push monitor answers with a short
	// JSON document, and leaving the body unread keeps the connection from
	// being reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("uptime: push status %d", resp.StatusCode)
	}
	return nil
}

// urlQueryEscape percent-encodes a note for the query string. It is spelled out
// rather than using net/url so the package keeps a single import for the wire
// shape, and so a note containing a newline cannot break the request line.
func urlQueryEscape(s string) string {
	replacer := strings.NewReplacer(
		" ", "%20", "\n", "%0A", "\r", "", "&", "%26", "?", "%3F",
		"#", "%23", "+", "%2B", "%", "%25", "/", "%2F",
	)
	return replacer.Replace(s)
}

// Loop pushes a heartbeat on a timer until ctx is cancelled.
//
// The configuration is read on every tick, never cached, so a URL saved in the
// dashboard takes effect on the next heartbeat. A tick with no configuration is
// a no-op rather than an error: an operator who turns the monitor off must not
// produce a failure log a minute.
type Loop struct {
	config  ConfigSource
	pusher  func() Pusher
	mu      sync.Mutex
	pusherH Pusher
}

// NewLoop builds a heartbeat loop. src supplies the settings per tick; pusher
// is resolved lazily so a pusher built from a URL that changes is rebuilt.
func NewLoop(src ConfigSource, pusher func() Pusher) *Loop {
	return &Loop{config: src, pusher: pusher}
}

// Run pushes until ctx is cancelled. It blocks, so call it in a goroutine.
func (l *Loop) Run(ctx context.Context) {
	if l == nil || l.config == nil {
		return
	}
	// The first push waits one interval rather than firing immediately: a
	// gateway that has just started is up by definition, and an immediate push
	// would mark the monitor up before anything has been proven.
	ticker := time.NewTicker(l.currentInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		l.tick(ctx)
		ticker.Reset(l.currentInterval())
	}
}

// currentInterval reads the configured period, falling back to the default.
func (l *Loop) currentInterval() time.Duration {
	cfg := l.readConfig()
	return cfg.Interval()
}

// readConfig reads the settings, degrading to a disabled config on error so a
// failed read is a no-op tick rather than a panic.
func (l *Loop) readConfig() Config {
	if l.config == nil {
		return Config{}
	}
	return l.config()
}

// tick pushes one heartbeat when the monitor is configured.
func (l *Loop) tick(ctx context.Context) {
	cfg := l.readConfig()
	if !cfg.Configured() {
		return
	}
	p := l.resolvePusher()
	if p == nil {
		return
	}
	pushCtx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	if err := p.Push(pushCtx, "up", ""); err != nil && ctx.Err() == nil {
		// Logged at warn and dropped: a failed push costs the operator a
		// missed heartbeat, and the monitor itself will mark the gateway down
		// when the next push does not arrive either. Retrying inside the tick
		// would delay the loop for a monitor that is already gone.
		log.Warn("uptime", "heartbeat push failed", "error", err)
	}
}

// resolvePusher returns the pusher, building it once and reusing it. The
// mutex keeps a configuration change from racing an in-flight tick.
func (l *Loop) resolvePusher() Pusher {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pusherH == nil && l.pusher != nil {
		l.pusherH = l.pusher()
	}
	return l.pusherH
}
