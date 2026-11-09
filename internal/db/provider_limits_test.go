package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

// seedProviderNode inserts a custom provider node row so its limit columns have
// a row to live on.
func seedProviderNode(t *testing.T, repo *Repo, id, name string) {
	t.Helper()
	if _, err := repo.CreateProviderNode(id, "openai-compatible", name,
		`{"prefix":"`+id+`","apiType":"openai-compatible","baseUrl":"https://example.test/v1"}`,
	); err != nil {
		t.Fatalf("CreateProviderNode %s: %v", id, err)
	}
}

// providerNodeHasColumn reports whether providerNodes carries the column, probed
// through pragma_table_info rather than by parsing a CREATE statement.
func providerNodeHasColumn(t *testing.T, repo *Repo, column string) bool {
	t.Helper()
	var count int
	if err := repo.RawDB().QueryRow(
		`SELECT COUNT(1) FROM pragma_table_info('providerNodes') WHERE name = ?`, column,
	).Scan(&count); err != nil {
		t.Fatalf("inspect providerNodes.%s: %v", column, err)
	}
	return count > 0
}

func TestProviderLimitColumnsExistAfterBootstrap(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	for _, column := range []string{
		"rpmLimit", "tpmLimit", "concurrencyLimit", "tokenQuota",
		"queueTimeoutMs", "quotaResetId", "contractExpiresAt",
	} {
		t.Run(column, func(t *testing.T) {
			if !providerNodeHasColumn(t, repo, column) {
				t.Errorf("providerNodes.%s is missing after EnsureCoreSchema", column)
			}
		})
	}
	var count int
	if err := repo.RawDB().QueryRow(
		`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'providerLedgerCarryover'`,
	).Scan(&count); err != nil {
		t.Fatalf("inspect providerLedgerCarryover: %v", err)
	}
	if count != 1 {
		t.Error("providerLedgerCarryover was not created by the ledger table bootstrap")
	}
}

func TestProviderLimits_RoundTrip(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	ctx := context.Background()
	seedProviderNode(t, repo, "node-nara", "Nara AI")

	want := ProviderLimits{
		RPM:               60,
		TPM:               90_000,
		Concurrency:       4,
		QueueTimeoutMs:    15_000,
		TokenQuota:        5_000_000,
		ContractExpiresAt: "2027-01-31T00:00:00Z",
	}
	if err := repo.SetProviderLimits(ctx, "node-nara", want); err != nil {
		t.Fatalf("SetProviderLimits: %v", err)
	}
	got, err := repo.GetProviderLimits(ctx, "node-nara")
	if err != nil {
		t.Fatalf("GetProviderLimits: %v", err)
	}
	if got != want {
		t.Errorf("GetProviderLimits = %+v, want %+v", got, want)
	}
}

func TestGetProviderLimits_UnknownNodeIsUnlimited(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	got, err := repo.GetProviderLimits(context.Background(), "node-missing")
	if err != nil {
		t.Fatalf("GetProviderLimits: %v", err)
	}
	if got != (ProviderLimits{}) {
		t.Errorf("limits of an unknown node = %+v, want the zero value", got)
	}
}

func TestListProviderLimits_CoversEveryNode(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	ctx := context.Background()
	seedProviderNode(t, repo, "node-nara", "Nara AI")
	seedProviderNode(t, repo, "node-kopi", "Kopi AI")
	if err := repo.SetProviderLimits(ctx, "node-kopi", ProviderLimits{RPM: 10}); err != nil {
		t.Fatalf("SetProviderLimits: %v", err)
	}

	all, err := repo.ListProviderLimits(ctx)
	if err != nil {
		t.Fatalf("ListProviderLimits: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("listed %d nodes, want 2: %+v", len(all), all)
	}
	if got := all["node-kopi"].RPM; got != 10 {
		t.Errorf("node-kopi RPM = %d, want 10", got)
	}
	if got := all["node-nara"]; got != (ProviderLimits{}) {
		t.Errorf("node-nara limits = %+v, want the zero value for a node never configured", got)
	}
}

func TestSetProviderLimits_RejectsNegativeCounts(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		limits ProviderLimits
	}{
		{"rpm", ProviderLimits{RPM: -1}},
		{"tpm", ProviderLimits{TPM: -1}},
		{"concurrency", ProviderLimits{Concurrency: -1}},
		{"token quota", ProviderLimits{TokenQuota: -1}},
		{"queue timeout", ProviderLimits{QueueTimeoutMs: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := openEphemeralSchemaDB(t)
			seedProviderNode(t, repo, "node-nara", "Nara AI")
			if err := repo.SetProviderLimits(ctx, "node-nara", tt.limits); err == nil {
				t.Fatalf("SetProviderLimits(%+v) accepted a negative count", tt.limits)
			}
			// The rejected write must not have landed: reading back has to still
			// show the node as never configured.
			got, err := repo.GetProviderLimits(ctx, "node-nara")
			if err != nil {
				t.Fatalf("GetProviderLimits: %v", err)
			}
			if got != (ProviderLimits{}) {
				t.Errorf("limits after a refused write = %+v, want the zero value", got)
			}
		})
	}
}

// TestSetProviderLimits_AcceptsZeroAsUnlimited pins the clear path: an operator
// removing a capacity limit writes 0, and the write must land rather than be
// refused. The dashboard only reaches this function when it sends an explicit
// patch (web/src/components/connections/ProviderDetailView.svelte), so a 0 here
// is a deliberate clear, not an absent field.
func TestSetProviderLimits_AcceptsZeroAsUnlimited(t *testing.T) {
	ctx := context.Background()
	repo := openEphemeralSchemaDB(t)
	seedProviderNode(t, repo, "node-nara", "Nara AI")

	if err := repo.SetProviderLimits(ctx, "node-nara", ProviderLimits{
		RPM:            60,
		TPM:            90_000,
		Concurrency:    4,
		TokenQuota:     5_000_000,
		QueueTimeoutMs: 15_000,
	}); err != nil {
		t.Fatalf("SetProviderLimits (seed): %v", err)
	}

	// Clearing every axis back to unlimited.
	if err := repo.SetProviderLimits(ctx, "node-nara", ProviderLimits{}); err != nil {
		t.Fatalf("SetProviderLimits (clear) = %v, want a stored clear, not a refusal", err)
	}
	got, err := repo.GetProviderLimits(ctx, "node-nara")
	if err != nil {
		t.Fatalf("GetProviderLimits: %v", err)
	}
	if got != (ProviderLimits{}) {
		t.Errorf("limits after clearing = %+v, want the zero value (unlimited on every axis)", got)
	}
}

func TestSetProviderLimits_NormalisesContractExpiry(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "utc passes through", input: "2027-01-31T00:00:00Z", want: "2027-01-31T00:00:00Z"},
		{name: "offset is converted to utc", input: "2027-01-31T07:00:00+07:00", want: "2027-01-31T00:00:00Z"},
		{name: "empty means no expiry", input: "", want: ""},
		{name: "unparseable is refused", input: "31-01-2027", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := openEphemeralSchemaDB(t)
			ctx := context.Background()
			seedProviderNode(t, repo, "node-nara", "Nara AI")

			err := repo.SetProviderLimits(ctx, "node-nara", ProviderLimits{ContractExpiresAt: tt.input})
			if (err != nil) != tt.wantErr {
				t.Fatalf("SetProviderLimits(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			got, err := repo.GetProviderLimits(ctx, "node-nara")
			if err != nil {
				t.Fatalf("GetProviderLimits: %v", err)
			}
			if got.ContractExpiresAt != tt.want {
				t.Errorf("stored contractExpiresAt = %q, want %q", got.ContractExpiresAt, tt.want)
			}
		})
	}
}

func TestSetProviderLimits_UnknownNode(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	err := repo.SetProviderLimits(context.Background(), "node-missing", ProviderLimits{RPM: 5})
	if !errors.Is(err, ErrNoSuchProviderNode) {
		t.Fatalf("SetProviderLimits on an unknown node = %v, want ErrNoSuchProviderNode", err)
	}
}

// TestSetProviderLimits_DoesNotMoveTheQuotaResetMark pins the ownership of
// quotaResetId: a policy save must not forgive spend (a mark moved forward) or
// re-charge it (a mark moved back). Only ResetProviderQuota writes it.
func TestSetProviderLimits_DoesNotMoveTheQuotaResetMark(t *testing.T) {
	repo := openEphemeralSchemaDB(t)
	ctx := context.Background()
	seedProviderNode(t, repo, "node-nara", "Nara AI")
	seedProviderLedgerRow(t, repo, 1, "node-nara", 300, 50, time.Now().UTC().Add(-time.Hour))

	mark, err := repo.ResetProviderQuota(ctx, "node-nara")
	if err != nil {
		t.Fatalf("ResetProviderQuota: %v", err)
	}
	if mark != 1 {
		t.Fatalf("reset mark = %d, want 1", mark)
	}

	// A policy save carries the whole struct, including the mark the caller read.
	if err := repo.SetProviderLimits(ctx, "node-nara", ProviderLimits{TokenQuota: 1_000, QuotaResetID: mark}); err != nil {
		t.Fatalf("SetProviderLimits: %v", err)
	}
	got, err := repo.ProviderQuotaResetID(ctx, "node-nara")
	if err != nil {
		t.Fatalf("ProviderQuotaResetID: %v", err)
	}
	if got != mark {
		t.Errorf("reset mark after a policy save = %d, want %d", got, mark)
	}
}

func TestProviderLimits_Expired(t *testing.T) {
	now := time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		expiresAt string
		want      bool
	}{
		{name: "no expiry", expiresAt: "", want: false},
		{name: "future contract", expiresAt: "2027-02-01T00:00:00Z", want: false},
		{name: "past contract", expiresAt: "2027-01-01T00:00:00Z", want: true},
		{name: "the exact moment counts as not yet expired", expiresAt: "2027-01-15T12:00:00Z", want: false},
		{name: "unparseable reads as no expiry", expiresAt: "soon", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limits := ProviderLimits{ContractExpiresAt: tt.expiresAt}
			if got := limits.Expired(now); got != tt.want {
				t.Errorf("Expired(%q) = %v, want %v", tt.expiresAt, got, tt.want)
			}
		})
	}
}
