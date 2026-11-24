package dashboard

import (
	"net/http"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// advancedModeSettingKey is the settings key that records whether this
// deployment exposes the operator surfaces. It is a deployment-level choice,
// not a per-key one: a personal gateway hides Plans and Persona because they
// answer a question a single operator does not have, and a resold gateway
// shows them because per-key budgets and prompt rewriting are the product.
//
// The first-run wizard asks once and writes this key; Settings can flip it
// afterwards. The sidebar already has its own hide list (`navVisibility`), so
// this key does not draw anything — it is the server-side half of the same
// decision, and the half a URL cannot walk around.
const advancedModeSettingKey = "advancedMode"

// personalModeHiddenNav is the sidebar hide list written when the operator
// picks Personal. Plans, the token saver and Persona are the three surfaces
// that only matter when someone else is spending the quota.
var personalModeHiddenNav = []string{"plans", "token-saver", "jailbreak"}

// advancedModeEnabled reports whether the operator surfaces are exposed.
//
// Default true: a database written before this key existed, or one the wizard
// never touched, keeps every route it always had. Only an explicit false —
// written by the wizard's Personal choice or by Settings — turns the surfaces
// off, so an upgrade cannot lock an operator out of Plans they already use.
func advancedModeEnabled(repo *db.Repo) bool {
	if repo == nil {
		return true
	}
	raw, err := repo.GetSettingsRaw()
	if err != nil || raw == nil {
		return true
	}
	if v, ok := raw[advancedModeSettingKey].(bool); ok {
		return v
	}
	return true
}

// applyAdvancedModeUpdate normalizes the wizard/Settings payload so a stored
// value is always a bool. A non-bool is dropped rather than coerced: a client
// that sends "false" as a string meant something, but guessing which is worse
// than leaving the previous value in place.
func applyAdvancedModeUpdate(updates map[string]any) {
	raw, present := updates[advancedModeSettingKey]
	if !present {
		return
	}
	v, ok := raw.(bool)
	if !ok {
		delete(updates, advancedModeSettingKey)
		return
	}
	updates[advancedModeSettingKey] = v
}

// AdvancedModeGate refuses the routes registered under it with 403 when the
// deployment runs in personal mode. The sidebar hides these entries; this is
// the half that also answers a request typed straight into the address bar, so
// "hidden" and "unavailable" agree.
func AdvancedModeGate(repo *db.Repo) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !advancedModeEnabled(repo) {
				noStore(w)
				handlerutil.WriteJSONError(w, http.StatusForbidden,
					"This feature is off in personal mode. Turn on advanced settings to use it.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
