package dashboard

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/auth"
	"9router/proxy/internal/config"
	"9router/proxy/internal/handlerutil"
)

// resetHint mirrors upstream RESET_HINT in src/app/api/auth/login/route.js.
const resetHint = "Forgot password? Reset to default via 9router-go CLI → Settings → Reset Password to Default."

// HandleAuthLogin handles POST /api/auth/login: verify the dashboard password
// and issue the session cookie. Mirrors upstream
// src/app/api/auth/login/route.js: progressive per-client lockout (429 +
// Retry-After), tunnel/tailscale gate, SSO-only gate, bcrypt hash first with
// INITIAL_PASSWORD / "123456" fallback, and no session cookie for a remote
// caller still on the well-known default password (CVE-2026-56679 class).
func (h *DashboardHandler) HandleAuthLogin(w http.ResponseWriter, r *http.Request) {
	ip := auth.LoginClientIP(r)
	if locked, retryAfter := auth.LoginLocked(ip); locked {
		writeLoginLocked(w, retryAfter)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writePlainError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var payload struct {
		Password    string `json:"password"`
		NewPassword string `json:"newPassword"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		writePlainError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	raw := settingsOrEmpty(h)

	if auth.TunnelLoginBlocked(r, raw) {
		writePlainError(w, http.StatusForbidden, "Dashboard access via tunnel is disabled")
		return
	}
	if msg, disabled := ssoPasswordDisabled(raw); disabled {
		writePlainError(w, http.StatusForbidden, msg)
		return
	}

	if h.verifyDashboardPassword(payload.Password) {
		auth.RecordLoginSuccess(ip)
		if payload.NewPassword != "" {
			if err := h.changeDashboardPassword(payload.Password, payload.NewPassword); err != nil {
				noStore(w)
				handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
					"success": false,
					"error":   err.Error(),
				})
				return
			}
			raw = settingsOrEmpty(h)
		} else if mustChangeDefaultPassword(r, raw) {
			noStore(w)
			handlerutil.WriteJSON(w, http.StatusForbidden, map[string]any{
				"success": false,
				"error": "Default password must be changed before remote access. " +
					"Change it from the local machine (or set INITIAL_PASSWORD).",
				"mustChangePassword": true,
			})
			return
		}
		token, err := auth.Sign(auth.Secret(), time.Now())
		if err != nil {
			writePlainError(w, http.StatusInternalServerError, "Failed to create session")
			return
		}
		auth.SetCookie(w, r, token)
		// Attribute the success to the session it just created, so an operator's
		// later actions trace back to this login in the audit log.
		h.RecordLogin(r, http.StatusOK, token)
		noStore(w)
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success":            true,
			"mustChangePassword": false,
		})
		return
	}

	remaining := auth.RecordLoginFail(ip)
	if locked, retryAfter := auth.LoginLocked(ip); locked {
		h.RecordLogin(r, http.StatusTooManyRequests, "")
		writeLoginLocked(w, retryAfter)
		return
	}
	h.RecordLogin(r, http.StatusUnauthorized, "")
	noStore(w)
	handlerutil.WriteJSON(w, http.StatusUnauthorized, map[string]any{
		"error":               "Invalid password. " + strconv.Itoa(remaining) + " attempt(s) left before lockout.",
		"remainingBeforeLock": remaining,
	})
}

// HandleAuthLogout handles POST /api/auth/logout: clear the session cookie.
func (h *DashboardHandler) HandleAuthLogout(w http.ResponseWriter, r *http.Request) {
	// Audit the logout before clearing the cookie: the actor is read from the
	// session that is about to disappear.
	h.RecordLogout(r)
	auth.ClearCookie(w)
	noStore(w)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleAuthStatus handles GET /api/auth/status (upstream parity).
func (h *DashboardHandler) HandleAuthStatus(w http.ResponseWriter, r *http.Request) {
	raw := settingsOrEmpty(h)
	claims := auth.SessionClaimSet(r)
	displayName, loginMethod := "Password user", "Password"
	var oidcName, oidcEmail, samlName, samlEmail *string
	var oidcLogin, samlLogin bool
	if claims != nil {
		switch {
		case claims.Saml:
			samlLogin = true
			loginMethod = "SAML"
			samlName, samlEmail = strPtr(claims.SamlName), strPtr(claims.SamlEmail)
			displayName = firstNonEmptyStr(claims.SamlName, claims.SamlEmail, "SAML user")
		case claims.Oidc:
			oidcLogin = true
			loginMethod = "OIDC"
			oidcName, oidcEmail = strPtr(claims.OidcName), strPtr(claims.OidcEmail)
			displayName = firstNonEmptyStr(claims.OidcName, claims.OidcEmail, "OIDC user")
		}
	}
	noStore(w)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"requireLogin":   auth.RequireLogin(h.Repo),
		"authMode":       stringOr(raw, "authMode", "password"),
		"ssoType":        stringOr(raw, "ssoType", "oidc"),
		"oidcConfigured": oidcConfigured(raw),
		"oidcLoginLabel": loginLabel(raw, "oidcLoginLabel", "Sign in with OIDC"),
		"samlConfigured": samlConfigured(raw),
		"samlLoginLabel": loginLabel(raw, "samlLoginLabel", "Sign in with SAML SSO"),
		"hasPassword":    hasStoredPassword(raw),
		"displayName":    displayName,
		"loginMethod":    loginMethod,
		"authenticated":  claims != nil,
		"oidcName":       oidcName,
		"oidcEmail":      oidcEmail,
		"oidcLogin":      oidcLogin,
		"samlName":       samlName,
		"samlEmail":      samlEmail,
		"samlLogin":      samlLogin,
	})
}

// HandleRequireLogin handles GET /api/settings/require-login. Unlike upstream
// it also returns `authenticated` so the SPA can trust the server about the
// session instead of a client-side flag.
func (h *DashboardHandler) HandleRequireLogin(w http.ResponseWriter, r *http.Request) {
	raw := settingsOrEmpty(h)
	tunnelAccess := true
	if v, ok := raw["tunnelDashboardAccess"].(bool); ok {
		tunnelAccess = v
	}
	noStore(w)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"requireLogin":          auth.RequireLogin(h.Repo),
		"tunnelDashboardAccess": tunnelAccess,
		"tunnelUrl":             stringOr(raw, "tunnelUrl", ""),
		"tailscaleUrl":          stringOr(raw, "tailscaleUrl", ""),
		"authenticated":         auth.SessionValid(r),
	})
}

// settingsOrEmpty loads the raw settings map, falling back to an empty map so a
// DB error degrades to "login required, no password" instead of panicking.
func settingsOrEmpty(h *DashboardHandler) map[string]any {
	raw, err := h.Repo.GetSettingsRaw()
	if err != nil || raw == nil {
		return map[string]any{}
	}
	return raw
}

func hasStoredPassword(raw map[string]any) bool {
	hash, _ := raw["password"].(string)
	return hash != ""
}

func stringOr(m map[string]any, key, fallback string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return fallback
}

// noStore marks auth responses uncacheable (upstream NO_STORE_HEADERS).
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

// writeLoginLocked answers 429 with the upstream lockout shape and header.
func writeLoginLocked(w http.ResponseWriter, retryAfter int) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	noStore(w)
	handlerutil.WriteJSON(w, http.StatusTooManyRequests, map[string]any{
		"error":      "Too many failed attempts. Try again in " + strconv.Itoa(retryAfter) + "s. " + resetHint,
		"retryAfter": retryAfter,
		"resetHint":  resetHint,
	})
}

// ssoPasswordDisabled mirrors upstream's SSO-only gate: when authMode is sso
// (or the legacy oidc/saml values) and the matching IdP is configured,
// password login is rejected with the provider-specific message.
func ssoPasswordDisabled(raw map[string]any) (string, bool) {
	mode, _ := raw["authMode"].(string)
	switch mode {
	case "sso", "saml", "oidc":
	default:
		return "", false
	}
	if ssoTypeOf(raw, mode) == "saml" {
		if samlConfigured(raw) {
			return "Password login is disabled. Use SAML SSO sign in.", true
		}
		return "", false
	}
	if oidcConfigured(raw) {
		return "Password login is disabled. Use OIDC sign in.", true
	}
	return "", false
}

// ssoTypeOf resolves the active SSO flavor, defaulting to oidc unless the
// legacy saml authMode (without an explicit ssoType) says otherwise.
func ssoTypeOf(raw map[string]any, authMode string) string {
	if v := strings.ToLower(strings.TrimSpace(stringOr(raw, "ssoType", ""))); v != "" {
		return v
	}
	if authMode == "saml" {
		return "saml"
	}
	return "oidc"
}

// oidcConfigured mirrors upstream isOidcConfigured: issuer + client ID +
// client secret must all be present.
func oidcConfigured(raw map[string]any) bool {
	return trimTrailingSlashes(stringOr(raw, "oidcIssuerUrl", "")) != "" &&
		strings.TrimSpace(stringOr(raw, "oidcClientId", "")) != "" &&
		strings.TrimSpace(stringOr(raw, "oidcClientSecret", "")) != ""
}

// samlConfigured mirrors upstream isSamlConfigured: entry point + IdP cert.
func samlConfigured(raw map[string]any) bool {
	return strings.TrimSpace(stringOr(raw, "samlEntryPoint", "")) != "" &&
		strings.TrimSpace(stringOr(raw, "samlCert", "")) != ""
}

func trimTrailingSlashes(v string) string {
	return strings.TrimRight(strings.TrimSpace(v), "/")
}

func loginLabel(raw map[string]any, key, fallback string) string {
	if v := strings.TrimSpace(stringOr(raw, key, "")); v != "" {
		return v
	}
	return fallback
}

// mustChangeDefaultPassword mirrors upstream's remote fresh-install guard: the
// well-known default password on a non-local connection forces a rotation
// before any session cookie is issued.
func mustChangeDefaultPassword(r *http.Request, raw map[string]any) bool {
	return !hasStoredPassword(raw) &&
		config.LoadConfig().InitialPassword == "" &&
		!nodeRequestIsLocal(r)
}

// strPtr maps an empty identity claim to JSON null (upstream status shape).
func strPtr(v string) *string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return &v
}
