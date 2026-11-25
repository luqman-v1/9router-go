package executor

import (
	"context"
	"errors"
	"time"
)

// Qoder catalog refresh gates — local compatibility fix.
//
// A credential whose catalog has never landed must not make every request
// pay for a fresh fetch (and its timeout) while upstream is unreachable: a
// failed attempt holds non-forced fetches off for a short window. A live
// snapshot is governed by qoderCatalogTTL alone, and forced fetches ignore
// the window entirely, so chat's last-resort refresh and the post-login fill
// always reach upstream.

// errQoderCatalogRetryHoldoff answers a non-forced fetch inside the retry
// window. Callers treat it like any fetch failure; chat's force-refresh path
// still runs, so a model request is never refused by the window alone.
var errQoderCatalogRetryHoldoff = errors.New("qoder: model catalogue fetch failed recently; retrying shortly")

// qoderCatalogRetryHoldoff reports whether a non-forced fetch must hold off.
// Only a credential without a live snapshot obeys the window: an expired but
// filled entry still refetches (its stale configs remain usable meanwhile),
// and a credential that never attempted a fetch is always due.
func qoderCatalogRetryHoldoff(creds QoderCosyCreds, region QoderRegion) bool {
	qoderCatalogMu.RLock()
	defer qoderCatalogMu.RUnlock()
	entry, ok := qoderCatalogs[qoderCatalogKey(creds, region)]
	if !ok || len(entry.rawConfigs) > 0 {
		return false
	}
	return time.Since(entry.attemptedAt) < qoderCatalogRetryWindow
}

// qoderRecordCatalogAttempt stamps the fetch start on the credential's entry,
// creating an empty placeholder when none exists. The placeholder carries no
// configs and a zero expiry, so every cache reader treats it as a miss while
// the retry window can still see the attempt. A failed fetch leaves it in
// place; a successful one replaces it with the full snapshot.
func qoderRecordCatalogAttempt(creds QoderCosyCreds, region QoderRegion) {
	qoderCatalogMu.Lock()
	defer qoderCatalogMu.Unlock()
	key := qoderCatalogKey(creds, region)
	entry, ok := qoderCatalogs[key]
	if !ok {
		qoderCatalogs[key] = &qoderCatalog{attemptedAt: time.Now()}
		return
	}
	entry.attemptedAt = time.Now()
}

// RefreshQoderCatalog force-fetches the live model list for one credential
// and replaces its cached snapshot. It is the post-login fill: a successful
// device login calls it so the operator's first chat request resolves
// model_config (and friendly names) from a catalog that is already warm.
// Failure leaves any existing snapshot in place and is reported, never
// fatal — the login itself has already succeeded.
func RefreshQoderCatalog(ctx context.Context, do QoderDoer, creds QoderCosyCreds, region QoderRegion) error {
	_, err := qoderFetchCatalog(ctx, do, creds, region, true)
	return err
}
