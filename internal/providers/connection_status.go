package providers

import (
	"encoding/json"
	"strings"
)

// Connection status vocabulary, mirroring the upstream Next.js 9router
// connection lifecycle (`src/sse/services/auth.js`):
//
//	active          -> a request succeeded, or the account recovered
//	unavailable     -> an auth/upstream failure locked the account
//	quota_exhausted -> upstream reported the quota/credit is exhausted
//	rate_limited    -> a rolling-window throttle is live
//	error           -> generic failure
//	expired         -> the stored OAuth token is past its expiry
//
// The dashboard must never equate "the toggle is on" with "the account works".
// Upstream does the same: `getStatusVariant(isActive, effectiveStatus)` in
// `src/shared/utils/connectionStatus.js` treats a connection that is not
// explicitly disabled as healthy only when the effective status is
// "active"/"success".
const (
	StatusActive         = "active"
	StatusUnavailable    = "unavailable"
	StatusQuotaExhausted = "quota_exhausted"
	StatusRateLimited    = "rate_limited"
	StatusError          = "error"
	StatusExpired        = "expired"
	StatusUnknown        = "unknown"
)

// EffectiveStatus resolves the status a connection should be presented with.
//
// A stale "unavailable" marker is not proof the account is broken: the lock
// that produced it may have expired (upstream clears it lazily on the next
// success). Upstream therefore downgrades "unavailable" back to "active" once
// no cooldown remains — see `providers/page.js`:
//
//	const effectiveStatus =
//	  conn.testStatus === "unavailable" && !isCooldown ? "active" : conn.testStatus;
//
// `hasActiveCooldown` carries that `isCooldown` bit: it is true when the
// connection still holds a live `modelLock_*` entry.
//
// One recovery is refused: a connection whose last error was a spent
// credit/quota budget reports "quota_exhausted" instead of "active". A lapsed
// timer does not refill a balance, so treating it as healthy would keep a
// guaranteed-failing account in the pool — the exact "33/33 active but nothing
// connects" confusion the operator hit.
func EffectiveStatus(isActive int, testStatus string, hasActiveCooldown bool) string {
	return EffectiveStatusWithError(isActive, testStatus, hasActiveCooldown, "")
}

// HasActiveCooldown reports whether a connection's data JSON carries any
// live modelLock_* entry. These are written by the chat path when a model
// returns 429/capacity, and cleared when the lock expires or the operator
// resets health. A live lock means the account is currently in a cooldown
// window for at least one model.
func HasActiveCooldown(dataJSON string) bool {
	if strings.TrimSpace(dataJSON) == "" {
		return false
	}
	// Fast path: scan the raw JSON for the prefix. This avoids unmarshalling
	// the whole blob on every status check, and is safe because modelLock_*
	// keys are always written by this codebase, never by user input.
	return strings.Contains(dataJSON, `"modelLock_`)
}

// ConnectionStatus resolves the effective status of a connection row,
// reading testStatus and lastError out of its data JSON blob. Callers that
// already hold the parsed fields should use EffectiveStatusWithError
// directly.
func ConnectionStatus(isActive int, dataJSON string) (status string, effectivelyActive bool) {
	if strings.TrimSpace(dataJSON) == "" {
		status = EffectiveStatus(isActive, "", false)
		return status, IsEffectivelyActive(isActive, "", false)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
		status = EffectiveStatus(isActive, "", false)
		return status, IsEffectivelyActive(isActive, "", false)
	}
	testStatus, _ := data["testStatus"].(string)
	lastError := errorTextOf(data["lastError"])
	status = EffectiveStatusWithError(isActive, testStatus, HasActiveCooldown(dataJSON), lastError)
	return status, IsEffectivelyActiveWithError(isActive, testStatus, HasActiveCooldown(dataJSON), lastError)
}

// errorTextOf flattens the two shapes lastError is stored in — a bare string
// and {message,error,code} — into one comparable string.
func errorTextOf(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if msg, ok := t["message"].(string); ok && msg != "" {
			return msg
		}
		if errText, ok := t["error"].(string); ok && errText != "" {
			return errText
		}
	}
	return ""
}

// EffectiveStatusWithError is EffectiveStatus plus the stored last error, so a
// quota exhaustion that outlives its cooldown is still reported as such.
func EffectiveStatusWithError(isActive int, testStatus string, hasActiveCooldown bool, lastError string) string {
	// A disabled connection reports its own state; the toggle wins.
	if isActive != 1 {
		return "disabled"
	}

	status := strings.ToLower(strings.TrimSpace(testStatus))
	// "unknown" is the value the recorder writes for a connection that has been
	// touched but never returned a verdict — it is the same situation as the
	// empty string below, just spelled out. Production carries it in bulk, and
	// because it is not a recognised case it fell through to the final
	// `return status` and was reported verbatim, so a healthy account read as
	// "unknown" and IsEffectivelyActive counted it as dead. Normalise it into
	// the not-yet-probed branch.
	if status == StatusUnknown {
		status = ""
	}
	if status == "" {
		// Never probed (a freshly added account) or probed before this build
		// started recording a verdict. An untested connection is not a broken
		// one: reporting it as a distinct unknown state made a healthy account
		// render with the same red "failed" treatment as a rejected one, so an
		// operator had to press Test on every new account before it looked
		// right.
		//
		// A stored lastError is still real evidence, so it is classified with
		// the same rules below before the account is called healthy. An empty
		// testStatus with a live cooldown or a spent balance is not "never
		// probed", it is "probed and failed".
		if hasActiveCooldown && LooksLikeThrottleWindow(lastError) {
			return StatusRateLimited
		}
		if LooksLikeQuotaExhausted(lastError) {
			return StatusQuotaExhausted
		}
		if hasActiveCooldown && LooksLikeRateLimited(lastError) {
			return StatusRateLimited
		}
		if LooksLikePermanentFailure(lastError) {
			return StatusUnavailable
		}
		if hasActiveCooldown {
			return StatusUnavailable
		}
		return StatusActive
	}

	// A short rolling window resets by itself. Checked before the quota rule: a
	// per-minute limit that also says "quota" is still a throttle, and telling
	// the operator to top up an account that only needs to wait is wrong advice.
	// Only while the lock is live, like every throttle: once it lapses the
	// account is usable again, and a forever-throttle would mark a recovered
	// account as broken.
	if hasActiveCooldown && LooksLikeThrottleWindow(lastError) {
		return StatusRateLimited
	}

	// A spent budget does not refill on a timer. Checked before the generic
	// rate-limit rule: most quota wording also contains "limit", and the harder
	// verdict must win.
	if LooksLikeQuotaExhausted(lastError) {
		return StatusQuotaExhausted
	}

	// A throttle clears on its own, but it is not the same as a broken account:
	// the operator needs to tell "wait a minute" from "this key is invalid".
	// Only while the lock is still live: once it lapses the account is usable
	// again, and reporting a permanent throttle would mark a recovered account
	// as broken forever.
	if hasActiveCooldown && LooksLikeRateLimited(lastError) {
		return StatusRateLimited
	}

	// A rejected credential does not heal on a timer either. Without this, a
	// 401/403/404 account with no live lock falls through to the stale-marker
	// recovery rule below and is reported as ACTIVE -- the dashboard then
	// claims a dead key is connected and keeps routing to it. Checked after the
	// throttle rule so a 429 body mentioning "not found" is not read as a dead
	// credential.
	if LooksLikePermanentFailure(lastError) {
		return StatusUnavailable
	}

	if status == StatusUnavailable && !hasActiveCooldown {
		return StatusActive
	}
	return status
}

// IsEffectivelyActive reports whether a connection should count as usable:
// enabled AND its effective status is a healthy one. This is the definition
// used for the "N of M active" counters and the category tickers, so the
// dashboard stops claiming credit-exhausted accounts are connected.
func IsEffectivelyActive(isActive int, testStatus string, hasActiveCooldown bool) bool {
	return IsEffectivelyActiveWithError(isActive, testStatus, hasActiveCooldown, "")
}

// IsEffectivelyActiveWithError is IsEffectivelyActive plus the stored error.
func IsEffectivelyActiveWithError(isActive int, testStatus string, hasActiveCooldown bool, lastError string) bool {
	switch EffectiveStatusWithError(isActive, testStatus, hasActiveCooldown, lastError) {
	case StatusActive, "success":
		return true
	default:
		return false
	}
}

// StatusVariant maps an effective status onto the three display buckets the
// upstream UI uses ("success", "error", "default").
func StatusVariant(isActive int, effectiveStatus string) string {
	if isActive != 1 {
		return "default"
	}
	switch effectiveStatus {
	case StatusActive, "success":
		return "success"
	case StatusRateLimited:
		// A throttle is temporary: amber, not red. Red trains the operator to
		// ignore it, and this account recovers on its own.
		return "warn"
	case StatusError, StatusExpired, StatusUnavailable, StatusQuotaExhausted:
		return "error"
	default:
		return "default"
	}
}
