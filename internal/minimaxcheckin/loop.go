package minimaxcheckin

import (
	"context"
	"time"

	"9router/proxy/internal/log"
)

// LoopInterval is how often accounts are checked. MiniMax's panel decides
// which day is claimable (is_today / status 2), so a 30-minute poll catches the
// rollover without assuming a local midnight; the claim itself only happens
// when the panel says the day is claimable, which is magpie's once-a-day claim.
const LoopInterval = 30 * time.Minute

// Config is the dynamic part of the loop, read from the settings row each tick.
type Config struct {
	Enabled bool
}

// AccountSource lists the credentials a check-in should run for.
type AccountSource func(ctx context.Context) ([]Account, error)

// Loop checks in every configured MiniMax Code account on an interval. It is
// bound to the gateway's shutdown context.
type Loop struct {
	checker  Checker
	config   func() Config
	accounts AccountSource
	ledger   *Ledger
	now      func() time.Time
}

// NewLoop builds the background loop. Passing nil config disables it, which
// keeps callers that have no settings repository safe.
func NewLoop(checker Checker, config func() Config, accounts AccountSource, ledger *Ledger) *Loop {
	if config == nil {
		config = func() Config { return Config{} }
	}
	return &Loop{checker: checker, config: config, accounts: accounts, ledger: ledger, now: time.Now}
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
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if cfg := l.config(); cfg.Enabled {
				l.tick(ctx)
			}
			timer.Reset(LoopInterval)
		}
	}
}

func (l *Loop) tick(ctx context.Context) {
	if l.config != nil && !l.config().Enabled {
		return
	}
	accounts, err := l.accounts(ctx)
	if err != nil {
		log.Warn("minimax-checkin", "list accounts", "error", err)
		return
	}
	for _, a := range accounts {
		if ctx.Err() != nil {
			return
		}
		r := l.checker.CheckMiniMaxCode(ctx, a)
		r.CheckedAt = l.now()
		if l.ledger != nil {
			if err := l.ledger.Record(r); err != nil {
				log.Warn("minimax-checkin", "record outcome", "provider", r.Provider, "account", r.Account, "error", err)
			}
		}
		switch r.Outcome {
		case OutcomeClaimed:
			log.Info("minimax-checkin", "claimed credits", "provider", r.Provider, "account", r.Account, "credit", r.Credit, "streak", r.Streak)
		case OutcomeFailed:
			log.Warn("minimax-checkin", "check-in failed", "provider", r.Provider, "account", r.Account, "error", r.Message)
		}
	}
}
