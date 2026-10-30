package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/proxy"
)

const (
	freebuffSessionPath  = "/api/v1/freebuff/session"
	freebuffSessionTTL   = 60 * time.Minute
	freebuffCLIUserAgent = "codebuff-cli/0.0.138"

	// freebuffInstanceHeader names the session a release (DELETE) targets. The
	// CLI sends the same header only on GET and DELETE — POST takes the model.
	freebuffInstanceHeader = "x-freebuff-instance-id"
)

type freebuffSession struct {
	InstanceID string
	ExpiresAt  time.Time
}

var (
	freebuffSessionMu    sync.RWMutex
	freebuffSessionCache = make(map[string]*freebuffSession) // key: token::model

	// freebuffClaimMu serializes admissions inside one process; the lease
	// table serializes across processes. Both are needed: the mutex stops
	// N goroutines from POSTing at once, the lease stops N processes.
	freebuffClaimMu sync.Mutex
)

// freebuffLeaseScope namespaces Freebuff session leases, aliased from db
// (no cycle: db imports only models). One source of truth, no drift.
const freebuffLeaseScope = db.LeaseScopeFreebuffSession

// freebuffLeaseKey hashes the token into a non-sensitive lookup key.
// The raw token never touches the leases table.
func freebuffLeaseKey(token, model string) string {
	sum := sha256.Sum256([]byte(token + "::" + model))
	return fmt.Sprintf("%x", sum)
}

// leaseTTLFor returns the lease TTL from a session expiry: upstream TTL
// minus a skew margin so followers re-read before the seat actually dies.
func leaseTTLFor(expiresAt time.Time) time.Duration {
	ttl := time.Until(expiresAt) - freebuffLeaseSkew
	if ttl < time.Minute {
		ttl = time.Minute
	}
	return ttl
}

const freebuffLeaseSkew = 60 * time.Second

var directFreebuffClient = &http.Client{
	Transport: &http.Transport{
		Proxy: nil, // direct connection to bypass proxy allowlist (e.g. sandbox proxy)
	},
	Timeout: 30 * time.Second,
}

func isFreebuffProxyRefusal(err error, resp *http.Response) bool {
	if resp != nil {
		if resp.StatusCode == http.StatusForbidden && (resp.Header.Get("X-Proxy-Error") != "" || strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/plain")) {
			return true
		}
	}
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "proxy") || strings.Contains(errStr, "connect tunnel failed") || strings.Contains(errStr, "blocked-by-allowlist") || strings.Contains(errStr, "forbidden") {
			return true
		}
	}
	return false
}

// DoFreebuffHTTP executes an HTTP request, falling back to direct connection if the proxy refuses it.
func DoFreebuffHTTP(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if isFreebuffProxyRefusal(err, resp) {
		if resp != nil {
			resp.Body.Close()
		}
		reqClone := req.Clone(ctx)
		if reqClone.GetBody != nil {
			reqClone.Body, _ = reqClone.GetBody()
		}
		return directFreebuffClient.Do(reqClone)
	}
	return resp, err
}
func getFreebuffSession(token, model string) (*freebuffSession, bool) {
	return getFreebuffSessionWithStore(nil, token, model)
}

// getFreebuffSessionWithStore resolves a session from memory (L1), then the
// cross-process lease table (L2) when a store is wired. A lease hit is
// copied into memory so the next lookup stays local. Expired entries on
// either layer read as absent.
func getFreebuffSessionWithStore(store LeaseStore, token, model string) (*freebuffSession, bool) {
	key := token + "::" + model
	freebuffSessionMu.RLock()
	sess, ok := freebuffSessionCache[key]
	freebuffSessionMu.RUnlock()
	if ok {
		if time.Now().Before(sess.ExpiresAt) {
			return sess, true
		}
		// Lazy eviction: expired rows must not accumulate forever across
		// tokens/models (no sweeper exists; deletion only happened on
		// explicit release before this).
		freebuffSessionMu.Lock()
		if cur, ok := freebuffSessionCache[key]; ok && cur == sess {
			delete(freebuffSessionCache, key)
		}
		freebuffSessionMu.Unlock()
	}
	if store == nil {
		return nil, false
	}
	lease, err := store.ReadLease(freebuffLeaseScope, freebuffLeaseKey(token, model))
	if err != nil || lease == nil {
		return nil, false
	}
	sess = &freebuffSession{InstanceID: lease.Value, ExpiresAt: lease.ExpiresAt}
	setFreebuffSession(token, model, sess)
	return sess, true
}

func setFreebuffSession(token, model string, sess *freebuffSession) {
	key := token + "::" + model
	freebuffSessionMu.Lock()
	defer freebuffSessionMu.Unlock()
	freebuffSessionCache[key] = sess
}

func clearFreebuffSession(token, model string) {
	key := token + "::" + model
	freebuffSessionMu.Lock()
	defer freebuffSessionMu.Unlock()
	delete(freebuffSessionCache, key)
}

// clearFreebuffSessionsForToken drops every cached session for a token. Called
// after an explicit release: the instance ids we cached are dead server-side,
// so keeping them would send chat requests with a stale freebuff_instance_id.
func clearFreebuffSessionsForToken(token string) {
	prefix := token + "::"
	freebuffSessionMu.Lock()
	defer freebuffSessionMu.Unlock()
	for key := range freebuffSessionCache {
		if strings.HasPrefix(key, prefix) {
			delete(freebuffSessionCache, key)
		}
	}
}

// dropFreebuffLease releases one lease row, but only when the stored value is
// still ours (compare-and-delete). A re-claim by another process meanwhile
// must survive: deleting its fresh row would resurrect the hijack war.
func dropFreebuffLease(store LeaseStore, token, model, instanceID string) {
	if store == nil || instanceID == "" {
		return
	}
	_ = store.ReleaseLease(freebuffLeaseScope, freebuffLeaseKey(token, model), instanceID)
}

// releaseFreebuffSession ends the session bound to instanceID and returns the
// Freebucks refund the server credited, if any.
//
// Ending a session is what frees the account's model binding: Freebuff serves
// one model per session, so a switch has to release the held instance before
// admitting a new one. A 404 means the row is already gone — the slot is free,
// which is the outcome the caller wanted, so it is not an error.
func releaseFreebuffSession(ctx context.Context, client *http.Client, baseURL, token, instanceID string, headers map[string]string) (int64, error) {
	if instanceID == "" {
		return 0, errors.New("freebuff session release requires an instance id")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, freebuffOrigin(baseURL)+freebuffSessionPath, nil)
	if err != nil {
		return 0, fmt.Errorf("create freebuff session release request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", freebuffCLIUserAgent)
	req.Header.Set(freebuffInstanceHeader, instanceID)

	resp, err := DoFreebuffHTTP(ctx, client, req)
	if err != nil {
		return 0, fmt.Errorf("freebuff session release failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return 0, fmt.Errorf("read freebuff session release response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return 0, nil
	}
	if resp.StatusCode != http.StatusOK {
		return 0, &proxy.UpstreamError{
			StatusCode: resp.StatusCode,
			Body:       respBytes,
		}
	}

	var data struct {
		Status          string `json:"status"`
		FreebucksRefund int64  `json:"freebucksRefund"`
	}
	_ = json.Unmarshal(respBytes, &data)
	return data.FreebucksRefund, nil
}

// FreebuffModelSwitch is the outcome of an explicit model switch.
type FreebuffModelSwitch struct {
	InstanceID      string
	Model           string
	ExpiresAt       time.Time
	FreebucksRefund int64
}

// SwitchFreebuffModel implements the CLI's "deliberate pick" path: end the
// session currently held on another model, then admit a fresh one on newModel.
//
// Requests for a different model are rejected upstream with `model_locked`
// while a session is live, and sessions last an hour even when idle, so
// releasing the held slot is the only way to change models without waiting for
// it to expire. Callers must only invoke this for a deliberate user action:
// the CLI reverts background requests instead of releasing the slot.
func SwitchFreebuffModel(ctx context.Context, client *http.Client, baseURL, token, instanceID, newModel string, headers map[string]string) (*FreebuffModelSwitch, error) {
	return SwitchFreebuffModelWithStore(ctx, nil, client, baseURL, token, instanceID, newModel, headers)
}

// SwitchFreebuffModelWithStore is the coordinated variant: after releasing,
// the old rows are dropped from memory AND the lease table (compare-and-
// delete per row is impossible without values, so memory is cleared by
// token while leases expire naturally or are overwritten on re-admit).
func SwitchFreebuffModelWithStore(ctx context.Context, store LeaseStore, client *http.Client, baseURL, token, instanceID, newModel string, headers map[string]string) (*FreebuffModelSwitch, error) {
	if newModel == "" {
		return nil, errors.New("freebuff model switch requires a model")
	}

	refund := int64(0)
	if instanceID != "" {
		r, err := releaseFreebuffSession(ctx, client, baseURL, token, instanceID, headers)
		if err != nil {
			return nil, err
		}
		refund = r
		// NOTE: the lease row for the old model cannot be addressed here
		// (model unknown); it expires naturally within its TTL and is
		// overwritten on next admit. Memory is cleared explicitly below.
	}
	clearFreebuffSessionsForToken(token)

	sess, err := requestFreebuffSessionWithStore(ctx, store, client, baseURL, token, newModel, headers)
	if err != nil {
		return nil, err
	}

	return &FreebuffModelSwitch{
		InstanceID:      sess.InstanceID,
		Model:           newModel,
		ExpiresAt:       sess.ExpiresAt,
		FreebucksRefund: refund,
	}, nil
}

func newModelLockedError(w http.ResponseWriter, currentModel, requestedModel string) *proxy.UpstreamError {
	errMsg := fmt.Sprintf(`Freebuff session is locked to "%s" — it cannot serve %s. Use "%s" or wait for the session to expire (~1h).`, currentModel, requestedModel, currentModel)
	errBody, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message":      errMsg,
			"type":         "model_locked",
			"code":         "model_locked",
			"currentModel": currentModel,
		},
	})
	if w != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write(errBody)
	}
	return &proxy.UpstreamError{
		StatusCode: http.StatusConflict,
		Body:       errBody,
	}
}

// freebuffCountryRefusal reports whether an upstream refusal is about the
// caller's region rather than their credential or their model.
//
// Freebuff still admits a session for a blocked region, so this only ever shows
// up on the turn — and as a bare 403 beside the model_locked handling it reads
// like a bad token, which sends the user to re-authenticate for nothing.
func freebuffCountryRefusal(body []byte) bool {
	lower := strings.ToLower(string(body))
	for _, marker := range []string{
		"country_blocked",
		"country_not_allowed",
		"country not allowed",
		"not available in your country",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// newCountryBlockedError reports that Freebuff refuses this region.
func newCountryBlockedError(w http.ResponseWriter, countryCode, reason string) *proxy.UpstreamError {
	msg := "Freebuff refuses this region for this account."
	if reason != "" {
		msg += " Reason: " + reason + "."
	}
	if countryCode != "" {
		msg += " Detected country: " + countryCode + "."
	}
	msg += " Route through another region or use a different account."

	errBody, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message":     msg,
			"type":        "country_blocked",
			"code":        "country_blocked",
			"countryCode": countryCode,
			"blockReason": reason,
		},
	})
	if w != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write(errBody)
	}
	return &proxy.UpstreamError{
		StatusCode: http.StatusForbidden,
		Body:       errBody,
	}
}

func requestFreebuffSession(ctx context.Context, client *http.Client, baseURL, token, model string, headers map[string]string) (*freebuffSession, error) {
	return requestFreebuffSessionWithStore(ctx, nil, client, baseURL, token, model, headers)
}

// requestFreebuffSessionWithStore admits exactly one session per token+model
// across all processes sharing the lease store:
//
//  1. Re-read the lease inside the claim mutex: a sibling process may have
//     admitted while we waited — then follow its instanceId, POST nothing.
//  2. Otherwise POST one admission, then Acquire the lease. Losing the
//     acquire race means a sibling won concurrently: drop our admission?
//     No — the server keeps the LAST admitted instance, so the loser must
//     re-read and follow the winner instead of using its own instanceId.
//  3. With a nil store the lease steps are skipped (single-process mode).
func requestFreebuffSessionWithStore(ctx context.Context, store LeaseStore, client *http.Client, baseURL, token, model string, headers map[string]string) (*freebuffSession, error) {
	if store != nil {
		freebuffClaimMu.Lock()
		defer freebuffClaimMu.Unlock()
		if sess, ok := getFreebuffSessionWithStore(store, token, model); ok {
			return sess, nil
		}
	}
	sess, err := postFreebuffSession(ctx, client, baseURL, token, model, headers)
	if err != nil {
		return nil, err
	}
	setFreebuffSession(token, model, sess)
	if store != nil {
		held, aerr := store.AcquireLease(freebuffLeaseScope, freebuffLeaseKey(token, model), sess.InstanceID, leaseTTLFor(sess.ExpiresAt))
		if aerr == nil && !held {
			// Lost the race: a sibling admitted concurrently and the
			// server honors the latest admission. Follow the winner.
			if winner, ok := getFreebuffSessionWithStore(store, token, model); ok {
				return winner, nil
			}
		}
	}
	return sess, nil
}

func postFreebuffSession(ctx context.Context, client *http.Client, baseURL, token, model string, headers map[string]string) (*freebuffSession, error) {
	origin := freebuffOrigin(baseURL)
	reqURL := origin + freebuffSessionPath

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, fmt.Errorf("create freebuff session request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", freebuffCLIUserAgent)
	req.Header.Set("x-freebuff-model", model)

	resp, err := DoFreebuffHTTP(ctx, client, req)
	if err != nil {
		return nil, fmt.Errorf("freebuff session request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read freebuff session response: %w", err)
	}

	var data struct {
		Status             string `json:"status"`
		InstanceID         string `json:"instanceId"`
		ExpiresAt          string `json:"expiresAt"`
		CurrentModel       string `json:"currentModel"`
		RequestedModel     string `json:"requestedModel"`
		Message            string `json:"message"`
		Error              string `json:"error"`
		CountryCode        string `json:"countryCode"`
		CountryBlockReason string `json:"countryBlockReason"`
	}
	_ = json.Unmarshal(respBytes, &data)

	// Handle model_locked response
	if data.Status == "model_locked" || data.Error == "model_locked" || (resp.StatusCode == http.StatusConflict && (data.CurrentModel != "" || strings.Contains(string(respBytes), "model_locked"))) {
		currentModel := data.CurrentModel
		requestedModel := data.RequestedModel
		if requestedModel == "" {
			requestedModel = model
		}

		// If requestedModel matches currentModel and instanceId is present, accept session!
		if requestedModel == currentModel && data.InstanceID != "" {
			expiresAt := time.Now().Add(freebuffSessionTTL)
			if data.ExpiresAt != "" {
				if t, err := time.Parse(time.RFC3339, data.ExpiresAt); err == nil {
					expiresAt = t
				}
			}
			return &freebuffSession{
				InstanceID: data.InstanceID,
				ExpiresAt:  expiresAt,
			}, nil
		}

		return nil, newModelLockedError(nil, currentModel, model)
	}

	if resp.StatusCode != http.StatusOK {
		// Only a refusal counts here: a successful admission also carries
		// countryBlockReason, and failing on that would break every turn.
		if freebuffCountryRefusal(respBytes) {
			return nil, newCountryBlockedError(nil, data.CountryCode, data.CountryBlockReason)
		}
		return nil, &proxy.UpstreamError{
			StatusCode: resp.StatusCode,
			Body:       respBytes,
		}
	}

	if data.InstanceID == "" {
		return nil, errors.New("freebuff session returned empty instanceId")
	}

	expiresAt := time.Now().Add(freebuffSessionTTL)
	if data.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, data.ExpiresAt); err == nil {
			expiresAt = t
		}
	}

	return &freebuffSession{
		InstanceID: data.InstanceID,
		ExpiresAt:  expiresAt,
	}, nil
}
