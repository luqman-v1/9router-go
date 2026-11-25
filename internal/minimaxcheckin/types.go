// Package minimaxcheckin claims MiniMax Code's daily check-in credits (每日签到).
// It ports the contract of magpie-community/plugins packages/minimax
// (index.mjs:861-905, 1201-1228): read the seven-day panel
// (/minimax-cloud/api/v1/signin/status), claim today's credits
// (/minimax-cloud/api/v1/signin/claim) when the day is claimable, and report
// claimed / done / inactive / ineligible / failed with the streak. Reference
// commit: afe4c086436099db7fb53b7e80ab54d04b654015.
//
// The signed account call itself lives in the dashboard handler, which already
// owns MiniMax Code's signed client and credential refresh; this package owns
// the background loop, the per-account ledger and the settings/accounts wiring.
// Both sites are kept strictly separate: minimax-code and minimax-code-global
// each use their own account host and their own stored credentials; neither is
// ever routed to the other's host (AGENTS.md section 3.A).
package minimaxcheckin

import (
	"context"
	"time"
)

// Outcome is the result of one account's check-in attempt. The names match
// magpie's checkinOf outcomes so a dashboard can present them directly.
type Outcome string

const (
	OutcomeClaimed    Outcome = "claimed"
	OutcomeDone       Outcome = "done"
	OutcomeInactive   Outcome = "inactive"
	OutcomeIneligible Outcome = "ineligible"
	OutcomeFailed     Outcome = "failed"
)

// Result is one account's latest check-in state.
type Result struct {
	Outcome   Outcome   `json:"outcome"`
	Message   string    `json:"message,omitempty"`
	Credit    float64   `json:"credit,omitempty"`
	Streak    int       `json:"streak,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
	Provider  string    `json:"provider"`
	Account   string    `json:"account"`
	AccountID string    `json:"accountId,omitempty"`
}

// Account is one MiniMax Code credential eligible for check-in. Only OAuth
// connections qualify: a desktop lease (mcode-lease) stores no token here, and
// magpie's checkin hook likewise requires an oauth sign-in.
type Account struct {
	Provider string // "minimax-code" or "minimax-code-global"
	ID       string // connection id, stable ledger key
	Name     string // display name
	Token    string // stored access token
	UserID   string // providerSpecificData.realUserID, when login kept it
}

// Checker performs one account's check-in. The dashboard handler implements it
// with the signed MiniMax Code account client and the shared credential refresh.
type Checker interface {
	CheckMiniMaxCode(ctx context.Context, a Account) Result
}
