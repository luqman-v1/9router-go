// Package qodercheckin claims Qoder's daily credits. It ports the contract of
// magpie-community/plugins packages/qoder (daily CAMPAIGNS list + one claim
// POST), referenced from magpie's Qoder provider after its built-in sign-in was
// retired. Reference commit: afe4c086436099db7fb53b7e80ab54d04b654015.
//
// Both deployments are kept strictly separate: qoder and qoder-cn each use
// their own openapi host and their own stored credentials; neither is ever
// routed to the other's host (AGENTS.md section 3.A).
package qodercheckin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/constants"
)

// CAMPAIGNS is where Qoder lists an account's campaigns, the daily credits
// among them (actionType CLAIM_BENEFIT), and claims one (POST …/{id}/claim).
const campaignsPath = "/sash/api/v1/me/campaigns"

// campaignURLs are the two sites' campaign pages, keyed by provider id.
var campaignURLs = map[string]string{
	"qoder":    "https://openapi.qoder.sh" + campaignsPath,
	"qoder-cn": "https://openapi.qoder.com.cn" + campaignsPath,
}

// Outcome is the result of one account's check-in attempt. The names match
// magpie's WorkBuddyCheckin outcomes so a dashboard can present them directly.
type Outcome string

const (
	OutcomeClaimed  Outcome = "claimed"
	OutcomeDone     Outcome = "done"
	OutcomeInactive Outcome = "inactive"
	OutcomeFailed   Outcome = "failed"
)

// Result is one account's latest check-in state.
type Result struct {
	Outcome    Outcome   `json:"outcome"`
	Message    string    `json:"message,omitempty"`
	Credit     float64   `json:"credit,omitempty"`
	ValidUntil time.Time `json:"validUntil,omitempty"`
	CheckedAt  time.Time `json:"checkedAt"`
	Provider   string    `json:"provider"`
	Account    string    `json:"account"`
}

// Account is one Qoder credential eligible for check-in.
type Account struct {
	Provider string // "qoder" or "qoder-cn"
	ID       string // stable account key (connection id)
	Name     string // display name
	// Token is the bearer token the campaign endpoints accept: the device
	// (access) token where present, otherwise the job token for PAT accounts.
	Token string
}

// valid filters unusable accounts before any network call.
func (a Account) valid() bool {
	if _, ok := campaignURLs[a.Provider]; !ok {
		return false
	}
	return strings.TrimSpace(a.Token) != "" && strings.TrimSpace(a.ID) != ""
}

type campaign struct {
	ID          string  `json:"campaignId"`
	ActionType  string  `json:"actionType"`
	ClaimStatus string  `json:"claimStatus"`
	StartAt     float64 `json:"startAt"`
	EndAt       float64 `json:"endAt"`
	Benefit     struct {
		Amount float64 `json:"amount"`
	} `json:"benefit"`
}

// active includes the start and excludes the end. Qoder's campaign window can
// cross Beijing midnight, so no local-day assumption is made here.
func (c campaign) active(now time.Time) bool {
	return !now.Before(qoderWhen(c.StartAt)) && now.Before(qoderWhen(c.EndAt))
}

// qoderWhen accepts Qoder's seconds or milliseconds timestamps.
func qoderWhen(v float64) time.Time {
	if v > 1e12 {
		return time.UnixMilli(int64(v))
	}
	return time.Unix(int64(v), 0)
}

type claimResponse struct {
	Status  string `json:"status"`
	Benefit struct {
		Amount float64 `json:"amount"`
	} `json:"benefit"`
	Data *claimResponse `json:"data"`
}

// Doer performs one outbound campaign request. Kept injectable so tests never
// reach the network; production passes a pooled http.Client's Do.
type Doer func(*http.Request) (*http.Response, error)

// Checker claims daily credits and records the outcome in a small JSON file.
type Checker struct {
	do    Doer
	now   func() time.Time
	store *store
}

// NewChecker builds a Checker. path is the ledger file (usually
// <DATA_DIR>/qoder-checkin.json); do may be nil to use a default client with a
// bounded timeout.
func NewChecker(path string, do Doer) (*Checker, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("qodercheckin: empty ledger path")
	}
	if do == nil {
		client := &http.Client{Timeout: 20 * time.Second}
		do = client.Do
	}
	st, err := newStore(path)
	if err != nil {
		return nil, err
	}
	return &Checker{do: do, now: time.Now, store: st}, nil
}

// Check claims all claimable daily campaigns for a, then returns the outcome.
// A campaign already claimed and still active counts as a completed check-in.
func (c *Checker) Check(ctx context.Context, a Account) Result {
	now := c.now()
	result := Result{Provider: a.Provider, Account: a.Name, CheckedAt: now, Outcome: OutcomeFailed}
	if !a.valid() {
		result.Message = "missing token or account id"
		return c.remember(result)
	}
	base, ok := campaignURLs[a.Provider]
	if !ok {
		result.Message = "unknown Qoder site"
		return c.remember(result)
	}

	var list struct {
		Campaigns []campaign `json:"campaigns"`
	}
	if err := c.call(ctx, a, http.MethodGet, base, &list); err != nil {
		result.Message = err.Error()
		return c.remember(result)
	}

	var daily []campaign
	for _, item := range list.Campaigns {
		if item.ActionType == "CLAIM_BENEFIT" {
			daily = append(daily, item)
		}
	}

	var credit float64
	var until time.Time
	claimed, failed := 0, 0
	var why string
	for _, item := range daily {
		if item.active(now) {
			if end := qoderWhen(item.EndAt); until.IsZero() || end.Before(until) {
				until = end
			}
		}
		if item.ClaimStatus != "CLAIMABLE" || item.ID == "" {
			continue
		}
		var got claimResponse
		target := base + "/" + url.PathEscape(item.ID) + "/claim"
		err := c.call(ctx, a, http.MethodPost, target, &got)
		if got.Data != nil {
			got = *got.Data
		}
		if err == nil && got.Status != "CLAIMED" {
			err = fmt.Errorf("the claim answered %q", got.Status)
		}
		if err != nil {
			failed++
			why = err.Error()
			continue
		}
		claimed++
		if got.Benefit.Amount > 0 {
			credit += got.Benefit.Amount
		} else {
			credit += item.Benefit.Amount
		}
	}

	switch {
	case failed > 0:
		result.Message = why
	case claimed > 0:
		result.Outcome, result.Credit, result.ValidUntil = OutcomeClaimed, credit, until
	case len(daily) == 0:
		result.Outcome = OutcomeInactive
	default:
		for _, item := range daily {
			if item.ClaimStatus == "CLAIMED" && item.active(now) {
				result.Outcome, result.Credit, result.ValidUntil = OutcomeDone, item.Benefit.Amount, until
				return c.remember(result)
			}
		}
		result.Message = "today's credits aren't offered yet"
	}
	return c.remember(result)
}

func (c *Checker) remember(r Result) Result {
	r.CheckedAt = c.now()
	if err := c.store.set(r); err != nil {
		// A failed ledger write must not flip an otherwise successful claim.
		// Leave the outcome intact; the next tick re-reads the account state.
		return r
	}
	return r
}

// call asks u as a and reads a bounded JSON answer.
func (c *Checker) call(ctx context.Context, a Account, method, u string, dst any) error {
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader("{}")
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(a.Token))
	res, err := c.do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	payload, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return errors.New("qoder sign-in has expired — sign in again")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(payload, &e) == nil {
			for _, m := range []string{e.Error.Message, e.Message} {
				if m = strings.TrimSpace(m); m != "" {
					return fmt.Errorf("HTTP %d: %s", res.StatusCode, m)
				}
			}
		}
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return json.Unmarshal(payload, dst)
}

// Results returns the last stored outcome per account, without network calls.
func (c *Checker) Results() []Result { return c.store.all() }

// --- ledger -----------------------------------------------------------------

type store struct {
	mu    sync.Mutex
	path  string
	byKey map[string]Result
}

func newStore(path string) (*store, error) {
	s := &store{path: path, byKey: map[string]Result{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("qodercheckin: read ledger: %w", err)
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &s.byKey); err != nil {
			// A corrupted ledger must not brick gateway boot. Start fresh; the
			// next successful check-in rewrites it atomically.
			s.byKey = map[string]Result{}
		}
	}
	return s, nil
}

func (s *store) key(r Result) string { return r.Provider + "\x00" + r.Account }

func (s *store) set(r Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byKey[s.key(r)] = r
	b, err := json.MarshalIndent(s.byKey, "", "  ")
	if err != nil {
		return fmt.Errorf("qodercheckin: marshal ledger: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), constants.FilePermDir); err != nil {
		return fmt.Errorf("qodercheckin: create ledger dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".qoder-checkin-*")
	if err != nil {
		return fmt.Errorf("qodercheckin: create temp ledger: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("qodercheckin: write ledger: %w", err)
	}
	if err := tmp.Chmod(constants.FilePermFile); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("qodercheckin: chmod ledger: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("qodercheckin: close ledger: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("qodercheckin: replace ledger: %w", err)
	}
	return nil
}

func (s *store) all() []Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Result, 0, len(s.byKey))
	for _, r := range s.byKey {
		out = append(out, r)
	}
	return out
}
