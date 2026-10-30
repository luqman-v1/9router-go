package db

import (
	"strings"
	"testing"
)

// TestUsageGroupByClientKeyQueryPlan proves the per-client-key read is
// answered through the ledger's timestamp index rather than a scan.
//
// A scan here is the regression the panel would carry on a big ledger: the
// Overview's other usage reads group the same window, and a panel that
// scans it pays a full pass every time the window changes.
func TestUsageGroupByClientKeyQueryPlan(t *testing.T) {
	r := openEphemeralSchemaDB(t)

	rows, err := r.db.Query(`EXPLAIN QUERY PLAN `+
		`SELECT COALESCE(clientKey, ''), `+usageGroupStats+`, `+usageClientKeyCachedTokens+
		` FROM usageHistory WHERE timestamp >= ? GROUP BY COALESCE(clientKey, '')`,
		"2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("explain query plan: %v", err)
	}
	defer rows.Close()

	var plans []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plans = append(plans, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate plan: %v", err)
	}

	joined := strings.Join(plans, "\n")
	if strings.Contains(joined, "SCAN usageHistory") {
		t.Errorf("the per-client-key read scans usageHistory instead of using the timestamp index:\n%s", joined)
	}
	if !strings.Contains(joined, "idx_uh_ts") {
		t.Errorf("the per-client-key read does not use the ledger's timestamp index:\n%s", joined)
	}
}

// TestUsageGroupByClientKeyReadsTheCachedTokens proves the cached counter is
// read from the row's tokens JSON, with the same precedence the row-level
// reader uses: the first shape that is PRESENT wins, and a present zero is a
// value rather than a reason to look further.
func TestUsageGroupByClientKeyReadsTheCachedTokens(t *testing.T) {
	r := openEphemeralSchemaDB(t)

	rows := []struct {
		tokens string
		want   int
	}{
		{`{"cached_tokens":400}`, 400},
		{`{"cache_read_input_tokens":350}`, 350},
		{`{"prompt_tokens_details":{"cached_tokens":300}}`, 300},
		{`{"input_tokens_details":{"cached_tokens":250}}`, 250},
		{`{"prompt_eval_cached_count":200}`, 200},
		// A present zero wins over the shapes below it: the row says the
		// provider reported no cache read, and that is the answer.
		{`{"cached_tokens":0,"cache_read_input_tokens":900}`, 0},
		{`{"other":1}`, 0},
		{`not json`, 0},
	}
	for i, row := range rows {
		if _, err := r.db.Exec(
			`INSERT INTO usageHistory (timestamp, provider, model, promptTokens, completionTokens, clientKey, tokens)
			 VALUES (?, 'p', 'm', 10, 5, ?, ?)`,
			"2026-01-02T00:00:00Z", "sk-client-"+string(rune('a'+i)), row.tokens,
		); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}

	got, err := r.GetUsageGroupByClientKeySince("2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("GetUsageGroupByClientKeySince: %v", err)
	}
	if len(got) != len(rows) {
		t.Fatalf("rows = %d, want %d", len(got), len(rows))
	}
	byKey := map[string]int{}
	for _, row := range got {
		byKey[row.ClientKey] = row.CachedTokens
	}
	for i, row := range rows {
		key := "sk-client-" + string(rune('a'+i))
		if byKey[key] != row.want {
			t.Errorf("row %d (%s) cachedTokens = %d, want %d", i, key, byKey[key], row.want)
		}
	}
}
