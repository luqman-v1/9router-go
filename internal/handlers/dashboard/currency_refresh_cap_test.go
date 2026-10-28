package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fullRateTable is a table at maxCurrencyRates that ends alphabetically in VND,
// so the cap's sorted truncation names a rate the operator typed by hand.
var fullRateTable = []string{"AUD", "BRL", "CAD", "CHF", "CNY", "CZK", "DKK", "EUR", "GBP", "HKD", "HUF", "IDR",
	"ILS", "INR", "JPY", "KRW", "MXN", "MYR", "NOK", "NZD", "PHP", "SEK", "SGD", "VND"}

// storeRateTable writes a display-currency table through the real settings
// handler, so the stored state is the one a refresh actually reads back.
func storeRateTable(t *testing.T, handler http.Handler, codes []string, manual map[string]float64) {
	t.Helper()
	rates := make(map[string]any, len(codes))
	for _, code := range codes {
		rates[code] = 1.0
	}
	for code, rate := range manual {
		rates[code] = rate
	}
	payload, err := json.Marshal(map[string]any{"displayCurrency": map[string]any{"code": "IDR", "rates": rates}})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/settings = %d: %s", rec.Code, rec.Body.String())
	}
}

// TestRefreshKeepsEveryStoredCodeWhenTheUnionOverflows pins the cap-pressure
// form of the data loss: the stored table already holds maxCurrencyRates codes,
// so a provider that publishes one more overflows the union, and the cap's
// sorted truncation then drops the alphabetically-last stored code — a manual
// rate. Which code is lost depends on where the fetched code sorts, so both
// directions are pinned: an early-sorting fetch is the case a late-sorting
// fixture hides.
func TestRefreshKeepsEveryStoredCodeWhenTheUnionOverflows(t *testing.T) {
	cases := map[string]map[string]any{
		"fetched sorts after the stored codes":  {"THB": 36.1},
		"fetched sorts before the stored codes": {"AED": 3.6725},
		"fetched publishes several new codes":   {"AED": 3.6725, "ZWL": 361.9},
	}
	for name, fetched := range cases {
		t.Run(name, func(t *testing.T) {
			repo, cleanup := setupSettingsTestDB(t)
			defer cleanup()
			storeRateTable(t, setupTestRouter(repo), fullRateTable, map[string]float64{"VND": 26000})

			merged, _ := mergeRefreshedRates(repo, fetched, nil)
			if merged == nil {
				t.Fatal("mergeRefreshedRates() = nil, want the overlaid table")
			}
			if got := rateValue(t, merged, "VND"); got != 26000 {
				t.Fatalf("manual VND rate = %v, want 26000 (merged = %s)", got, strings.Join(rateKeys(merged), ","))
			}
			if len(merged) > maxCurrencyRates {
				t.Fatalf("merged carries %d codes, want at most %d", len(merged), maxCurrencyRates)
			}
		})
	}
}

// TestRefreshKeepsTheStoredTableWhenTheProviderPublishesNothingUsable covers the
// degenerate side of the same cap: a refresh that adds no code must return the
// stored table untouched rather than a re-truncated copy of it.
func TestRefreshKeepsTheStoredTableWhenTheProviderPublishesNothingUsable(t *testing.T) {
	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	storeRateTable(t, setupTestRouter(repo), fullRateTable, map[string]float64{"VND": 26000})

	merged, _ := mergeRefreshedRates(repo, map[string]any{}, nil)
	if got := rateValue(t, merged, "VND"); got != 26000 {
		t.Fatalf("manual VND rate = %v, want 26000 (merged = %s)", got, strings.Join(rateKeys(merged), ","))
	}
	if len(merged) != len(fullRateTable) {
		t.Fatalf("merged has %d codes, want the stored %d unchanged", len(merged), len(fullRateTable))
	}
}
