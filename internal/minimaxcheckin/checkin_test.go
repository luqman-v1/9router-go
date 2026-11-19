package minimaxcheckin

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"9router/proxy/internal/models"
)

type fakeChecker struct {
	calls   int
	outcome Outcome
}

func (f *fakeChecker) CheckMiniMaxCode(ctx context.Context, a Account) Result {
	f.calls++
	return Result{Outcome: f.outcome, Credit: 20, Streak: 1, Provider: a.Provider, Account: a.Name, AccountID: a.ID}
}

func TestLedger_RecordAndResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkin.json")
	l, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Results(); len(got) != 0 {
		t.Fatalf("fresh ledger has %d results", len(got))
	}
	now := time.Now()
	if err := l.Record(Result{AccountID: "c1", Provider: "minimax-code", Account: "a", Outcome: OutcomeClaimed, Credit: 20, CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := l.Record(Result{AccountID: "c2", Provider: "minimax-code-global", Account: "b", Outcome: OutcomeDone, CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	// A second ledger on the same file reads what the first wrote.
	l2, err := NewLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	got := l2.Results()
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	// Updating c1 replaces, not duplicates.
	if err := l2.Record(Result{AccountID: "c1", Provider: "minimax-code", Account: "a", Outcome: OutcomeDone, CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	if len(l2.Results()) != 2 {
		t.Fatalf("update duplicated the entry: %d", len(l2.Results()))
	}
}

func TestLoop_TickClaimsEnabledAccounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkin.json")
	l, _ := NewLedger(path)
	fc := &fakeChecker{outcome: OutcomeClaimed}
	accounts := func(ctx context.Context) ([]Account, error) {
		return []Account{
			{Provider: "minimax-code", ID: "c1", Name: "a", Token: "t"},
			{Provider: "minimax-code-global", ID: "c2", Name: "b", Token: "t"},
		}, nil
	}
	loop := NewLoop(fc, func() Config { return Config{Enabled: true} }, accounts, l)
	loop.tick(context.Background())
	if fc.calls != 2 {
		t.Fatalf("checker called %d times, want 2", fc.calls)
	}
	if len(l.Results()) != 2 {
		t.Fatalf("ledger has %d results, want 2", len(l.Results()))
	}
}

func TestLoop_DisabledDoesNothing(t *testing.T) {
	fc := &fakeChecker{outcome: OutcomeClaimed}
	loop := NewLoop(fc, func() Config { return Config{Enabled: false} }, func(ctx context.Context) ([]Account, error) {
		return []Account{{Provider: "minimax-code", ID: "c1", Token: "t"}}, nil
	}, nil)
	loop.tick(context.Background())
	if fc.calls != 0 {
		t.Fatalf("disabled loop called the checker %d times", fc.calls)
	}
}

func TestAccountFromConnection(t *testing.T) {
	name, email := "acct", "a@b.c"
	conn := &models.ProviderConnection{
		ID: "c1", Name: &name, Email: &email, AuthType: "oauth",
		Data: `{"accessToken":"at","refreshToken":"rt","providerSpecificData":{"realUserID":"42"}}`,
	}
	a := AccountFromConnection("minimax-code", conn)
	if a.Token != "at" || a.UserID != "42" || a.Name != "acct" {
		t.Fatalf("got %+v", a)
	}
	// No stored token (a lease) yields an empty Token.
	lease := &models.ProviderConnection{ID: "c2", AuthType: "oauth", Data: `{}`}
	if AccountFromConnection("minimax-code", lease).Token != "" {
		t.Error("lease should have no token")
	}
	// Email is the fallback name.
	fallback := "x@y.z"
	noName := &models.ProviderConnection{ID: "c3", Email: &fallback, AuthType: "oauth", Data: `{"accessToken":"t"}`}
	if AccountFromConnection("minimax-code", noName).Name != "x@y.z" {
		t.Error("email should be the fallback name")
	}
}

func TestConfigFromRepo_NilIsDisabled(t *testing.T) {
	if ConfigFromRepo(nil)().Enabled {
		t.Error("nil repo should be disabled")
	}
}

func TestRepoAccounts_NilIsEmpty(t *testing.T) {
	got, err := RepoAccounts(nil)(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("nil repo: got %v, %v", got, err)
	}
}
