package qodercheckin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
)

// LoopInterval is how often campaigns are checked. Qoder's daily campaign
// window can cross Beijing midnight, so a 30-minute poll (not a local-midnight
// timer) is what catches the rollover, matching magpie's 30-minute check.
const LoopInterval = 30 * time.Minute

// Config is the dynamic part of the loop, read from the settings row each tick.
type Config struct {
	Enabled bool
}

// AccountSource lists the credentials a check-in should run for.
type AccountSource func(ctx context.Context) ([]Account, error)

// Loop checks in every configured Qoder account on an interval and at each
// Beijing-day change. It is bound to the gateway's shutdown context.
type Loop struct {
	checker  *Checker
	config   func() Config
	accounts AccountSource
	now      func() time.Time
	// beijing fixes the zone for tests; production uses Asia/Shanghai loaded
	// once from the IANA database (no zoneinfo.zip dependency at runtime).
	beijing *time.Location
}

// NewLoop builds the background loop. Passing nil config disables it, which
// keeps callers that have no settings repository safe.
func NewLoop(checker *Checker, config func() Config, accounts AccountSource) *Loop {
	if config == nil {
		config = func() Config { return Config{} }
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		// A stripped system without zoneinfo still gets a correct 30-minute
		// poll; the day-boundary extra wake-up is simply skipped.
		loc = nil
	}
	return &Loop{checker: checker, config: config, accounts: accounts, now: time.Now, beijing: loc}
}

// Run blocks until ctx is cancelled. The first tick runs immediately on boot
// so a claim missed while the gateway was down is recovered without waiting a
// full interval.
func (l *Loop) Run(ctx context.Context) {
	if l == nil || l.checker == nil {
		return
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	lastDay := ""
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-timer.C:
			day := l.beijingDay(now)
			if day != lastDay {
				lastDay = day
			}
			if cfg := l.config(); cfg.Enabled {
				l.tick(ctx)
			}
			next := LoopInterval
			if delay := l.untilBeijingMidnight(now); delay > 0 && delay < LoopInterval {
				next = delay
			}
			timer.Reset(next)
		}
	}
}

func (l *Loop) beijingDay(now time.Time) string {
	if l.beijing == nil {
		return ""
	}
	return now.In(l.beijing).Format("2006-01-02")
}

func (l *Loop) untilBeijingMidnight(now time.Time) time.Duration {
	if l.beijing == nil {
		return 0
	}
	local := now.In(l.beijing)
	midnight := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, l.beijing)
	return time.Until(midnight)
}

func (l *Loop) tick(ctx context.Context) {
	accounts, err := l.accounts(ctx)
	if err != nil {
		log.Warn("qoder-checkin", "list accounts", "error", err)
		return
	}
	for _, a := range accounts {
		if ctx.Err() != nil {
			return
		}
		r := l.checker.Check(ctx, a)
		switch r.Outcome {
		case OutcomeClaimed:
			log.Info("qoder-checkin", "claimed credits", "provider", r.Provider, "account", r.Account, "credit", r.Credit)
		case OutcomeFailed:
			log.Warn("qoder-checkin", "check-in failed", "provider", r.Provider, "account", r.Account, "error", r.Message)
		}
	}
}

// RepoAccounts builds an account source from the stored provider connections.
// Credentials stay in the database; only the bearer token is copied into the
// request, and qoder/qoder-cn never share one another's credentials.
func RepoAccounts(repo *db.Repo) AccountSource {
	return func(ctx context.Context) ([]Account, error) {
		var out []Account
		for _, provider := range []string{"qoder", "qoder-cn"} {
			conns, err := repo.GetProviderConnections(provider, true)
			if err != nil {
				return nil, fmt.Errorf("qodercheckin: list %s: %w", provider, err)
			}
			for _, conn := range conns {
				if token := connectionToken(conn); token != "" {
					out = append(out, Account{
						Provider: provider,
						ID:       conn.ID,
						Name:     connectionName(conn),
						Token:    token,
					})
				}
			}
		}
		return out, nil
	}
}

func connectionName(conn *models.ProviderConnection) string {
	if conn.Name != nil && strings.TrimSpace(*conn.Name) != "" {
		return strings.TrimSpace(*conn.Name)
	}
	if conn.Email != nil && strings.TrimSpace(*conn.Email) != "" {
		return strings.TrimSpace(*conn.Email)
	}
	return conn.ID
}

// connectionToken picks the token the campaign endpoints accept. Device
// tokens (accessToken) are the account's own; PAT accounts have only a job
// token (apiKey after the exchange), which magpie also accepts for check-in.
func connectionToken(conn *models.ProviderConnection) string {
	var data map[string]any
	if err := json.Unmarshal([]byte(conn.Data), &data); err != nil {
		return ""
	}
	for _, key := range []string{"accessToken", "deviceToken", "jobToken", "apiKey"} {
		if s, ok := data[key].(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
		}
	}
	return ""
}

// ConfigFromRepo reads the qoderCheckin setting without caching it, so a
// dashboard toggle takes effect on the next tick.
func ConfigFromRepo(repo *db.Repo) func() Config {
	return func() Config {
		if repo == nil {
			return Config{}
		}
		raw, err := repo.GetSettingsRaw()
		if err != nil {
			return Config{}
		}
		enabled, _ := raw["qoderCheckin"].(bool)
		return Config{Enabled: enabled}
	}
}
