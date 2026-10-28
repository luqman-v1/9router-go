package dashboard

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"9router/proxy/internal/auth"
	"9router/proxy/internal/config"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/thinking"
)

// Dashboard client headers, matching the Next dashboard settings/database route.
const (
	cliTokenHeader  = "x-9r-cli-token"
	passwordHeader  = "x-9r-password"
	proxyDefaultURL = "https://google.com/"
	proxyMaxTimeout = 30 * time.Second
	proxyTimeout    = 8 * time.Second
	// defaultInitialPassword mirrors upstream DEFAULT_PASSWORD ("123456"):
	// accepted when no bcrypt hash is stored and INITIAL_PASSWORD is unset.
	defaultInitialPassword = "123456"
)

// protectedSettingKeys may never be written by the dashboard client — matches
// Next's PROTECTED_SETTING_KEYS (the hashed password lives in settings too).
var protectedSettingKeys = []string{"password", "mitmSudoEncrypted"}

// writePlainError answers with Next's flat { error: "message" } shape instead of
// the OpenAI-style envelope, because the dashboard UI reads data.error as text.
func writePlainError(w http.ResponseWriter, status int, message string) {
	handlerutil.WriteJSON(w, status, map[string]any{"error": message})
}

// secretSettingKeys are stripped from responses so credentials never leave the
// server (Next strips password + oidcClientSecret from GET /api/settings).
var secretSettingKeys = []string{"password", "oidcClientSecret"}

// HandleGetSettings handles GET /api/settings.
// Reads raw settings data map minus secret keys, mirroring the Next dashboard.
func (h *DashboardHandler) HandleGetSettings(w http.ResponseWriter, r *http.Request) {
	raw, err := h.Repo.GetSettingsRaw()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, sanitizeSettings(raw))
}

// HandleUpdateSettings handles PUT/PATCH /api/settings.
// Password changes are hashed here so the plaintext never reaches storage.
func (h *DashboardHandler) HandleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var updates map[string]any
	if err := json.Unmarshal(body, &updates); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if updates == nil {
		updates = map[string]any{}
	}

	// Password change carries current/new instead of a direct `password` write.
	if _, wantsPasswordChange := updates["newPassword"]; wantsPasswordChange {
		newPassword, _ := updates["newPassword"].(string)
		currentPassword, _ := updates["currentPassword"].(string)
		delete(updates, "currentPassword")
		delete(updates, "newPassword")
		if newPassword == "" {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "newPassword is required")
			return
		}
		if err := h.changeDashboardPassword(currentPassword, newPassword); err != nil {
			handlerutil.WriteJSONError(w, http.StatusUnauthorized, err.Error())
			return
		}
	} else {
		delete(updates, "currentPassword")
		delete(updates, "newPassword")
	}

	for _, key := range protectedSettingKeys {
		if _, ok := updates[key]; ok {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "setting "+key+" is read-only")
			return
		}
	}

	if err := validateProviderThinking(updates["providerThinking"]); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	normalizeProviderThinking(updates)

	if err := validateNumericSettings(updates); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Feature Visibility stores sidebar ids, so the payload is normalized here:
	// the sidebar must never receive an id it cannot address (navvisibility.go).
	applyNavVisibilityUpdate(updates)
	// Display currency stores a code plus a rate table, so the payload is
	// normalized here: a cost surface must never read a NaN rate (currency.go).
	applyDisplayCurrencyUpdate(updates)

	// Auto-ping schedules are merged against the stored value rather than
	// written through: a client that sends only `connections` would otherwise
	// delete every schedule, and one that sends only `schedules` would wipe
	// every switch. Refused here, before the write, so a bad payload leaves
	// the settings row untouched.
	if err := h.mergeAutoPingSchedules(updates); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// White-label branding is rendered by the sidebar, so the stored shape is
	// normalized here the same way the sidebar ids are (whitelabel.go).
	applyWhitelabelUpdate(updates)
	// Clearing appLogoUrl removes the uploaded file with it. The previous value
	// is read before the write so the path is known, and the file is removed
	// only after the write succeeds: a failed write must leave the stored logo
	// and its file in agreement, not one pointing at a deleted asset.
	previousLogo, clearLogo := logoClearedByUpdate(h, updates)
	if err := h.Repo.UpdateSettingsRaw(updates); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if clearLogo {
		removeUploadedLogo(previousLogo)
	}

	if h.TokenSaver != nil {
		if s, err := h.Repo.GetSettings(); err == nil && s != nil {
			h.TokenSaver.UpdateFromSettings(s)
		}
	}

	updated, err := h.Repo.GetSettingsRaw()
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, sanitizeSettings(updated))
}

// HandleExportDatabase handles GET /api/settings/database (backup download).
//
// A dashboard session authorizes this on its own, the same way every other
// dashboard read does. Demanding the re-typed password header as well meant the
// download only worked for a caller able to set a custom header, so a plain
// browser link — or any instance reached through a reverse proxy that strips
// it — was refused with 401 and the export looked permanently blocked (issue #47).
// The password header stays as the step-up path for a script that already has it.
//
// The payload is shipped as pretty-printed JSON with a download filename, the
// way upstream answers the profile page's export button, so a saved backup is
// readable without unzipping it (issue #160). Import still accepts both the JSON
// download and the older .zip archives.
func (h *DashboardHandler) HandleExportDatabase(w http.ResponseWriter, r *http.Request) {
	if !h.exportAuthorized(r) {
		writePlainError(w, http.StatusUnauthorized, "Invalid password")
		return
	}

	payload, err := h.exportDatabase(r.Context())
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, "Failed to export database")
		return
	}

	nowStr := time.Now().Format("2006-01-02")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="9router-backup-%s.json"`, nowStr))
	handlerutil.WriteJSONIndented(w, http.StatusOK, payload)
}

// HandleImportDatabase handles POST /api/settings/database (backup restore).
func (h *DashboardHandler) HandleImportDatabase(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var payload map[string]any

	// Support zip archives (PK\x03\x04 header)
	if len(body) >= 4 && body[0] == 'P' && body[1] == 'K' && body[2] == 3 && body[3] == 4 {
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			writePlainError(w, http.StatusBadRequest, "Invalid zip archive")
			return
		}
		var jsonBytes []byte
		for _, f := range zr.File {
			if strings.HasSuffix(strings.ToLower(f.Name), ".json") {
				rc, err := f.Open()
				if err != nil {
					continue
				}
				jsonBytes, _ = io.ReadAll(io.LimitReader(rc, 64<<20))
				rc.Close()
				break
			}
		}
		if len(jsonBytes) == 0 {
			writePlainError(w, http.StatusBadRequest, "No json file found inside zip archive")
			return
		}
		if err := json.Unmarshal(jsonBytes, &payload); err != nil || payload == nil {
			writePlainError(w, http.StatusBadRequest, "Invalid database payload inside zip")
			return
		}
	} else {
		if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
			writePlainError(w, http.StatusBadRequest, "Invalid database payload")
			return
		}
	}

	password, _ := payload["password"].(string)
	delete(payload, "password")

	if !trustedRequest(r) && !h.verifyDashboardPassword(password) &&
		!h.verifyDashboardPassword(r.Header.Get(passwordHeader)) {
		writePlainError(w, http.StatusUnauthorized, "Invalid password")
		return
	}

	dryRun := isDryRun(r)
	report, err := h.importDatabase(r.Context(), payload, dryRun)
	if err != nil {
		writePlainError(w, http.StatusBadRequest, err.Error())
		return
	}

	// upstream re-applies the outbound proxy env here; the Go runtime has no
	// equivalent yet (no outboundProxyUrl handling), so nothing to re-apply.

	if dryRun {
		report["success"] = true
		report["dryRun"] = true
		handlerutil.WriteJSON(w, http.StatusOK, report)
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleProxyTest handles POST /api/settings/proxy-test: HEAD a test URL
// through the candidate proxy and report reachability + latency.
func (h *DashboardHandler) HandleProxyTest(w http.ResponseWriter, r *http.Request) {
	reqBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writePlainError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	var body struct {
		ProxyURL  string `json:"proxyUrl"`
		TestURL   string `json:"testUrl"`
		TimeoutMS int    `json:"timeoutMs"`
	}
	if err := json.Unmarshal(reqBytes, &body); err != nil {
		writePlainError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	proxyURL := strings.TrimSpace(body.ProxyURL)
	if proxyURL == "" {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "proxyUrl is required"})
		return
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid proxy URL: " + proxyURL})
		return
	}

	testURL := strings.TrimSpace(body.TestURL)
	if testURL == "" {
		testURL = proxyDefaultURL
	}
	timeout := proxyTimeout
	if body.TimeoutMS > 0 {
		timeout = time.Duration(body.TimeoutMS) * time.Millisecond
		if timeout > proxyMaxTimeout {
			timeout = proxyMaxTimeout
		}
	}

	tr := proxy.NewSafeTransportForProxy(parsed.String())
	client := &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}
	started := time.Now()
	req, err := http.NewRequest(http.MethodHead, testURL, nil)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	req.Header.Set("User-Agent", "9Router")

	resp, err := client.Do(req)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	elapsedMs := time.Since(started).Milliseconds()
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	if !ok {
		// Next reports the upstream status back so the UI can show it.
		handlerutil.WriteJSON(w, resp.StatusCode, map[string]any{"ok": false, "error": "Proxy test failed"})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"status":     resp.StatusCode,
		"statusText": strings.TrimPrefix(resp.Status, " "),
		"url":        testURL,
		"elapsedMs":  elapsedMs,
	})
}

// changeDashboardPassword verifies currentPassword against the stored hash (or
// accepts an empty value on first-time set, plus the well-known "123456" the
// way Next's PATCH /api/settings does) and stores a bcrypt hash of the new
// one.
func (h *DashboardHandler) changeDashboardPassword(currentPassword, newPassword string) error {
	raw, err := h.Repo.GetSettingsRaw()
	if err != nil || raw == nil {
		raw = map[string]any{}
	}
	hash, _ := raw["password"].(string)
	if hash != "" {
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(currentPassword)); err != nil {
			return errors.New("Invalid current password")
		}
	} else {
		initial := config.LoadConfig().InitialPassword
		if initial == "" {
			initial = defaultInitialPassword
		}
		if currentPassword != "" && currentPassword != initial && currentPassword != defaultInitialPassword {
			return errors.New("Invalid current password")
		}
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return h.Repo.UpdateSettingsRaw(map[string]any{"password": string(hashed)})
}

// verifyDashboardPassword mirrors Next's verifyDashboardPassword: a stored
// bcrypt hash wins, otherwise INITIAL_PASSWORD wins, otherwise the well-known
// "123456" default (upstream DEFAULT_PASSWORD) is accepted.
func (h *DashboardHandler) verifyDashboardPassword(password string) bool {
	if password == "" {
		return false
	}
	if raw, err := h.Repo.GetSettingsRaw(); err == nil {
		if hash, _ := raw["password"].(string); hash != "" {
			return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
		}
	}
	initial := config.LoadConfig().InitialPassword
	if initial == "" {
		initial = defaultInitialPassword
	}
	return subtle.ConstantTimeCompare([]byte(initial), []byte(password)) == 1
}

// trustedRequest reports whether the caller is a local CLI client, which the
// Next dashboard trusts without re-prompting for the password. The header
// value is checked against the derived machine token — never trusted by
// presence — so remote callers cannot bypass with an arbitrary value.
func trustedRequest(r *http.Request) bool {
	return auth.ValidCLIToken(r.Header.Get(cliTokenHeader))
}

// exportAuthorized gates the backup download. A dashboard session is enough on
// its own, matching every other dashboard read; the re-typed password header and
// the local CLI token remain valid ways in for a caller that is not carrying a
// session, and an instance with no password set at all has nothing to protect.
func (h *DashboardHandler) exportAuthorized(r *http.Request) bool {
	if trustedRequest(r) {
		return true
	}
	if h.verifyDashboardPassword(r.Header.Get(passwordHeader)) {
		return true
	}
	return auth.SessionValid(r) || !auth.RequireLogin(h.Repo)
}

// sanitizeSettings copies settings and drops secrets, exposing `hasPassword`
// the way the Next dashboard does.
// validateProviderThinking rejects a per-provider reasoning override whose
// mode is outside the allowed vocabulary. The mode reaches reasoning_effort on
// the wire, and a provider answers an unknown effort with a 400 — so a mode
// that cannot be honoured is refused at the boundary instead.
func validateProviderThinking(v any) error {
	if v == nil {
		return nil
	}
	pt, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("providerThinking must be an object")
	}
	for provider, entry := range pt {
		vm, ok := entry.(map[string]any)
		if !ok {
			return fmt.Errorf("providerThinking[%s] must be an object", provider)
		}
		mode, _ := vm["mode"].(string)
		if _, valid := thinking.NormalizeProviderThinkingMode(mode); !valid {
			return fmt.Errorf("providerThinking[%s].mode must be one of %s", provider, strings.Join(thinking.ProviderThinkingModes, ", "))
		}
	}
	return nil
}

// numericSettingKeys are the settings the dashboard writes as numbers.
//
// A value of another type is refused at the boundary: the reader coerces
// anything that is not a positive number to zero, and zero is "no limit" on
// the rate limiter. A quota written as a string would therefore read back as
// unlimited while the rate limiter stays on.
var numericSettingKeys = []string{
	"defaultRpm", "defaultTpm", "defaultConcurrency", "rateWindowSeconds",
	"retentionDays", "maxRequestRecords", "usageHistoryRetentionDays",
	"rtkMaxLines", "rtkMaxChars", "headroomTimeoutMs",
	"semanticCacheTTL", "semanticCacheMaxEntries",
	"stickyRoundRobinLimit", "comboStickyRoundRobinLimit",
}

func validateNumericSettings(updates map[string]any) error {
	for _, key := range numericSettingKeys {
		v, ok := updates[key]
		if !ok || v == nil {
			continue
		}
		switch v.(type) {
		case float64, int, int64:
		default:
			return fmt.Errorf("setting %s must be a number", key)
		}
	}
	return nil
}

// normalizeProviderThinking rewrites the stored modes into the vocabulary the
// enforcement uses, so the dashboard echoes back the mode that is enforced
// rather than the spelling the operator typed.
func normalizeProviderThinking(updates map[string]any) {
	pt, ok := updates["providerThinking"].(map[string]any)
	if !ok {
		return
	}
	for _, entry := range pt {
		vm, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		mode, _ := vm["mode"].(string)
		if normalized, valid := thinking.NormalizeProviderThinkingMode(mode); valid {
			vm["mode"] = normalized
		}
	}
}

// sanitizeSettings copies settings and drops secrets, exposing `hasPassword`
// the way the Next dashboard does.
func sanitizeSettings(raw map[string]any) map[string]any {
	if raw == nil {
		raw = map[string]any{}
	}
	out := make(map[string]any, len(raw)+1)
	for k, v := range raw {
		out[k] = v
	}
	hasPassword := false
	if hash, ok := raw["password"].(string); ok && hash != "" {
		hasPassword = true
	}
	for _, key := range secretSettingKeys {
		delete(out, key)
	}
	sanitizeNavVisibility(out)
	sanitizeDisplayCurrency(out)
	sanitizeWhitelabel(out)
	out["hasPassword"] = hasPassword
	return out
}

// stripSecretSettings copies settings and drops every secret key, reusing
// secretSettingKeys so the list stays single-sourced. Unlike sanitizeSettings
// it adds no derived `hasPassword` field: the backup payload is read back by
// importDatabase, so it must carry stored settings only.
func stripSecretSettings(raw map[string]any) map[string]any {
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		out[k] = v
	}
	for _, key := range secretSettingKeys {
		delete(out, key)
	}
	return out
}

// readSettingsSecrets returns the current value of every secret setting, read
// inside the caller's transaction before the settings row is wiped. Imports
// need it because a backup no longer carries these keys.
func readSettingsSecrets(tx *sql.Tx) (map[string]any, error) {
	var data string
	err := tx.QueryRow(`SELECT data FROM settings WHERE id = 1`).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	raw := map[string]any{}
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil, err
	}
	secrets := make(map[string]any, len(secretSettingKeys))
	for _, key := range secretSettingKeys {
		if v, ok := raw[key]; ok && v != nil {
			secrets[key] = v
		}
	}
	return secrets, nil
}

// databaseExport is the backup payload shape shared with the Next dashboard so
// a backup taken on one runtime restores on the other.
type databaseExport struct {
	Settings            map[string]any   `json:"settings,omitempty"`
	ProviderConnections []map[string]any `json:"providerConnections"`
	ProviderNodes       []map[string]any `json:"providerNodes"`
	ProxyPools          []map[string]any `json:"proxyPools"`
	APIKeys             []map[string]any `json:"apiKeys"`
	Combos              []map[string]any `json:"combos"`
	ModelAliases        map[string]any   `json:"modelAliases"`
	CustomModels        []any            `json:"customModels"`
	MitmAlias           map[string]any   `json:"mitmAlias"`
	Pricing             map[string]any   `json:"pricing"`
}

// queryer is the read surface shared by *sql.DB and *sql.Tx, so the export
// walk can run either directly or inside one snapshot transaction.
type queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// exportDatabase reads the dashboard tables into the shared backup payload.
//
// Every read happens inside one transaction. Reading table by table on a bare
// *sql.DB would let a write land between two queries, so a key deleted between
// the apiKeys query and the access query would export as a key with no
// allowlist — which restores as unrestricted. The transaction pins one
// snapshot for the whole walk, so the backup is a state the database was
// actually in.
func (h *DashboardHandler) exportDatabase(ctx context.Context) (*databaseExport, error) {
	rawDB := h.Repo.RawDB()
	tx, err := rawDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // read-only; nothing to salvage

	out := &databaseExport{
		ProviderConnections: []map[string]any{},
		ProviderNodes:       []map[string]any{},
		ProxyPools:          []map[string]any{},
		APIKeys:             []map[string]any{},
		Combos:              []map[string]any{},
		ModelAliases:        map[string]any{},
		CustomModels:        []any{},
		MitmAlias:           map[string]any{},
		Pricing:             map[string]any{},
	}

	settings, err := getSettingsRawTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if len(settings) > 0 {
		out.Settings = stripSecretSettings(settings)
	}

	if out.ProviderConnections, err = selectRows(tx,
		`SELECT id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt FROM providerConnections`); err != nil {
		return nil, err
	}
	for _, row := range out.ProviderConnections {
		mergeDataColumn(row, "id", "provider", "authType", "name", "email", "priority", "isActive", "createdAt", "updatedAt")
		normalizeBool(row, "isActive")
	}

	if out.ProviderNodes, err = selectRows(tx,
		`SELECT id, type, name, data, createdAt, updatedAt,
		        rpmLimit, tpmLimit, concurrencyLimit, tokenQuota, queueTimeoutMs, quotaResetId,
		        COALESCE(contractExpiresAt, '') AS contractExpiresAt
		   FROM providerNodes`); err != nil {
		return nil, err
	}
	for _, row := range out.ProviderNodes {
		// The limit columns are real columns, so mergeDataColumn needs them named
		// as well: it rebuilds each row from the data blob overlaid with the listed
		// columns, and a column named in neither place would drop out of the backup
		// while the restore still looked successful.
		mergeDataColumn(row, "id", "type", "name", "createdAt", "updatedAt",
			"rpmLimit", "tpmLimit", "concurrencyLimit", "tokenQuota", "queueTimeoutMs",
			"quotaResetId", "contractExpiresAt")
	}

	if out.ProxyPools, err = selectRows(tx,
		`SELECT id, isActive, testStatus, data, createdAt, updatedAt FROM proxyPools`); err != nil {
		return nil, err
	}
	for _, row := range out.ProxyPools {
		mergeDataColumn(row, "id", "isActive", "testStatus", "createdAt", "updatedAt")
		normalizeBool(row, "isActive")
	}

	if out.APIKeys, err = selectRows(tx,
		`SELECT id, key, name, machineId, isActive, createdAt,
		        rateLimitRPM, rateLimitTPM, rateLimitConcurrency, tokenQuota, queueTimeoutMs
		   FROM apiKeys`); err != nil {
		return nil, err
	}
	// The per-key model allowlist is a separate table, so selecting apiKeys
	// alone drops it. A restored key would then silently allow every model
	// again — the one outcome a restriction exists to prevent.
	access, err := selectRows(tx,
		`SELECT api_key_id, model FROM api_key_model_access ORDER BY api_key_id, model`)
	if err != nil {
		return nil, err
	}
	allowlists := make(map[string][]string)
	for _, row := range access {
		keyID, _ := row["api_key_id"].(string)
		model, _ := row["model"].(string)
		if keyID != "" && model != "" {
			allowlists[keyID] = append(allowlists[keyID], model)
		}
	}
	for _, row := range out.APIKeys {
		normalizeBool(row, "isActive")
		models := []string{}
		if id, _ := row["id"].(string); id != "" && len(allowlists[id]) > 0 {
			models = allowlists[id]
		}
		// Both shapes are always emitted. `allowedModels` is what a Go instance
		// reads back; `access` is what the Node dashboard reads. Emitting only
		// one of them means a backup taken here restores unrestricted there.
		row["allowedModels"] = models
		row["access"] = accessShape(models)
	}

	if out.Combos, err = selectRows(tx,
		`SELECT id, name, kind, models, matchRules, createdAt, updatedAt FROM combos`); err != nil {
		return nil, err
	}
	for _, row := range out.Combos {
		row["models"] = parseJSONValue(row["models"], []any{})
	}

	if err := loadKVScopes(tx, out); err != nil {
		return nil, err
	}
	// Nothing was written, so the commit only releases the snapshot. It is
	// still required: the deferred Rollback would undo nothing but would end
	// the transaction all the same.
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// getSettingsRawTx reads the settings blob inside an existing transaction, so
// the export's settings and its table walks come from the same snapshot.
func getSettingsRawTx(ctx context.Context, tx *sql.Tx) (map[string]any, error) {
	var rawData string
	err := tx.QueryRowContext(ctx, `SELECT data FROM settings WHERE id = 1`).Scan(&rawData)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if rawData == "" {
		return map[string]any{}, nil
	}
	raw := map[string]any{}
	if err := json.Unmarshal([]byte(rawData), &raw); err != nil || raw == nil {
		return map[string]any{}, nil
	}
	return raw, nil
}

// importDatabase wipes the dashboard tables and restores a backup payload,
// matching Next's importDb transaction.
//
// A dry run walks the identical transaction and rolls it back, so the report
// describes what a real import would do without writing anything. Rolling back
// is what makes that true: every statement runs, so a payload that would fail
// halfway fails here too instead of looking clean.
func (h *DashboardHandler) importDatabase(ctx context.Context, payload map[string]any, dryRun bool) (map[string]any, error) {
	rawDB := h.Repo.RawDB()
	tx, err := rawDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Secrets are stripped from the backup, so a restore must not let their
	// absence delete the live credentials: wiping the settings row without
	// them would silently reset the dashboard to the default password and
	// disable OIDC. Read them inside the transaction before the wipe.
	liveSecrets, err := readSettingsSecrets(tx)
	if err != nil {
		return nil, err
	}

	wipes := []string{
		`DELETE FROM settings`,
		`DELETE FROM providerConnections`,
		`DELETE FROM providerNodes`,
		`DELETE FROM proxyPools`,
		`DELETE FROM apiKeys`,
		// Without this the access rows outlive their key and accumulate as
		// orphans after every import.
		`DELETE FROM api_key_model_access`,
		`DELETE FROM combos`,
		`DELETE FROM kv WHERE scope IN ('modelAliases', 'customModels', 'mitmAlias', 'pricing')`,
	}
	for _, stmt := range wipes {
		if _, err := tx.Exec(stmt); err != nil {
			return nil, err
		}
	}

	if settings, ok := payload["settings"].(map[string]any); ok && len(settings) > 0 {
		restored := make(map[string]any, len(settings)+len(liveSecrets))
		for k, v := range settings {
			restored[k] = v
		}
		// A backup that predates the export sanitiser — or one taken from a
		// different runtime — still wins; only a missing key falls back to
		// the live value. An empty value means "no password here", not
		// "reset this machine to the default", so it falls back too.
		for k, v := range liveSecrets {
			if s, provided := restored[k].(string); !provided || s == "" {
				restored[k] = v
			}
		}
		b, err := json.Marshal(restored)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(
			`INSERT INTO settings(id, data) VALUES(1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, string(b),
		); err != nil {
			return nil, err
		}
	}

	connections, _ := payload["providerConnections"].([]any)
	for _, item := range connections {
		c, ok := item.(map[string]any)
		if !ok {
			continue
		}
		data := restData(c, "id", "provider", "authType", "name", "email", "priority", "isActive", "createdAt", "updatedAt")
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO providerConnections(id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c["id"], c["provider"], stringDefault(c["authType"], "oauth"), stringDefault(c["name"], nil),
			stringDefault(c["email"], nil), c["priority"], boolToInt(c["isActive"]), data,
			stringDefault(c["createdAt"], nowISO()), stringDefault(c["updatedAt"], nowISO()),
		); err != nil {
			return nil, err
		}
	}

	nodes, _ := payload["providerNodes"].([]any)
	for _, item := range nodes {
		n, ok := item.(map[string]any)
		if !ok {
			continue
		}
		// A restored node must come back with the limits it had: a backup that
		// silently lifted a node's ceiling would turn disaster recovery into an
		// accidental opening of the throttle, the same reason apiKeys carries its
		// own limits below. quotaResetId travels too — it is the mark the lifetime
		// quota is measured from, and restoring the spend without the mark would
		// charge the node for tokens it had already been forgiven.
		data := restData(n, "id", "type", "name", "createdAt", "updatedAt",
			"rpmLimit", "tpmLimit", "concurrencyLimit", "tokenQuota", "queueTimeoutMs",
			"quotaResetId", "contractExpiresAt")
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO providerNodes(id, type, name, data, createdAt, updatedAt,
			        rpmLimit, tpmLimit, concurrencyLimit, tokenQuota, queueTimeoutMs, quotaResetId,
			        contractExpiresAt)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			n["id"], stringDefault(n["type"], nil), stringDefault(n["name"], nil), data,
			stringDefault(n["createdAt"], nowISO()), stringDefault(n["updatedAt"], nowISO()),
			intDefault(n["rpmLimit"], 0), intDefault(n["tpmLimit"], 0),
			intDefault(n["concurrencyLimit"], 0), intDefault(n["tokenQuota"], 0),
			intDefault(n["queueTimeoutMs"], 0), intDefault(n["quotaResetId"], 0),
			stringDefault(n["contractExpiresAt"], ""),
		); err != nil {
			return nil, err
		}
	}

	pools, _ := payload["proxyPools"].([]any)
	for _, item := range pools {
		p, ok := item.(map[string]any)
		if !ok {
			continue
		}
		data := restData(p, "id", "isActive", "testStatus", "createdAt", "updatedAt")
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO proxyPools(id, isActive, testStatus, data, createdAt, updatedAt) VALUES(?, ?, ?, ?, ?, ?)`,
			p["id"], boolToInt(p["isActive"]), stringDefault(p["testStatus"], "unknown"), data,
			stringDefault(p["createdAt"], nowISO()), stringDefault(p["updatedAt"], nowISO()),
		); err != nil {
			return nil, err
		}
	}

	keys, _ := payload["apiKeys"].([]any)
	for _, item := range keys {
		k, ok := item.(map[string]any)
		if !ok {
			continue
		}
		keyID, _ := k["id"].(string)
		if keyID == "" {
			continue
		}
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO apiKeys(id, key, name, machineId, isActive, createdAt,
			        rateLimitRPM, rateLimitTPM, rateLimitConcurrency, tokenQuota, queueTimeoutMs)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			k["id"], k["key"], stringDefault(k["name"], nil), stringDefault(k["machineId"], nil),
			boolToInt(k["isActive"]), stringDefault(k["createdAt"], nowISO()),
			// A restored key must come back with the limits it had: a backup
			// that silently lifted every rate limit and token quota would turn
			// disaster recovery into an accidental opening of the throttle.
			// Missing columns coerce to 0 (unlimited), which is the same
			// meaning an absent column has on the live row.
			intDefault(k["rateLimitRPM"], 0), intDefault(k["rateLimitTPM"], 0),
			intDefault(k["rateLimitConcurrency"], 0), intDefault(k["tokenQuota"], 0),
			intDefault(k["queueTimeoutMs"], 0),
		); err != nil {
			return nil, err
		}
		for _, model := range importAllowlist(k) {
			if _, err := tx.Exec(
				`INSERT OR REPLACE INTO api_key_model_access(api_key_id, model, created_at) VALUES(?, ?, ?)`,
				keyID, model, stringDefault(k["createdAt"], nowISO()),
			); err != nil {
				return nil, err
			}
		}
	}

	combos, _ := payload["combos"].([]any)
	for _, item := range combos {
		c, ok := item.(map[string]any)
		if !ok {
			continue
		}
		models, err := json.Marshal(c["models"])
		if err != nil {
			models = []byte("[]")
		}
		rules, err := json.Marshal(c["matchRules"])
		if err != nil || string(rules) == "null" {
			rules = []byte("[]")
		}
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO combos(id, name, kind, models, matchRules, createdAt, updatedAt) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			c["id"], c["name"], stringDefault(c["kind"], nil), string(models), string(rules),
			stringDefault(c["createdAt"], nowISO()), stringDefault(c["updatedAt"], nowISO()),
		); err != nil {
			return nil, err
		}
	}

	if err := writeKVPayload(tx, "modelAliases", payload["modelAliases"], true); err != nil {
		return nil, err
	}
	if err := writeKVPayload(tx, "customModels", payload["customModels"], false); err != nil {
		return nil, err
	}
	if err := writeKVPayload(tx, "mitmAlias", payload["mitmAlias"], true); err != nil {
		return nil, err
	}
	if err := writeKVPayload(tx, "pricing", payload["pricing"], true); err != nil {
		return nil, err
	}

	report := importReport(payload)
	report["warnings"] = headerLimitWarnings(payload)
	if dryRun {
		// Roll back rather than commit: the deferred Rollback above is what
		// undoes every statement, so the report describes real work that was
		// then discarded instead of a separate estimate that could disagree
		// with what the import would actually do.
		return report, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return report, nil
}

// headerLimitWarnings reports provider overrides that break the G17 limits.
//
// The header is reported, never trimmed: an override that goes on the wire
// must go on the wire whole, and a silently truncated value would change what
// the upstream receives. The operator decides what to do with the warning.
func headerLimitWarnings(payload map[string]any) []any {
	settings, _ := payload["settings"].(map[string]any)
	overrides, _ := settings["providerOverrides"].(map[string]any)
	warnings := []any{}
	for connID, raw := range overrides {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		headers, _ := entry["headers"].(map[string]any)
		if len(headers) > db.MaxOverrideHeaders {
			warnings = append(warnings, map[string]any{
				"code":         "incompatible_header_limit",
				"connectionId": connID,
				"headerName":   "",
				"length":       len(headers),
				"limit":        db.MaxOverrideHeaders,
				"reason":       "too many headers",
			})
		}
		for name, rawValue := range headers {
			value, ok := rawValue.(string)
			if !ok || len(value) <= db.MaxOverrideHeaderValueSize {
				continue
			}
			warnings = append(warnings, map[string]any{
				"code":         "incompatible_header_limit",
				"connectionId": connID,
				"headerName":   name,
				"length":       len(value),
				"limit":        db.MaxOverrideHeaderValueSize,
				"reason":       "header value too long",
			})
		}
	}
	return warnings
}

// importReport summarizes what a payload carried, counting what was actually
// accepted rather than what was present: a malformed row is skipped by the
// import loops and must not inflate the count.
func importReport(payload map[string]any) map[string]any {
	report := map[string]any{}
	accesses := map[string]int{}
	for _, table := range []string{"providerConnections", "providerNodes", "proxyPools", "apiKeys", "combos"} {
		items, _ := payload[table].([]any)
		accepted := 0
		for _, item := range items {
			row, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if table == "apiKeys" {
				if id, _ := row["id"].(string); id == "" {
					continue
				}
				models := importAllowlist(row)
				switch {
				case len(models) == 0:
					accesses["unrestricted"]++
				case len(models) == 1 && models[0] == db.SentinelDenyAll:
					accesses["restrictedEmpty"]++
				default:
					accesses["restrictedList"]++
				}
			}
			accepted++
		}
		report[table] = accepted
	}
	report["apiKeyAccess"] = accesses
	return report
}

// writeKVPayload restores kv rows for one scope. keyed scopes store a map of
// key → value; customModels stores a list keyed by providerAlias|id|type.
func writeKVPayload(tx *sql.Tx, scope string, value any, keyed bool) error {
	if keyed {
		m, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		for k, v := range m {
			b, err := json.Marshal(v)
			if err != nil {
				continue
			}
			if _, err := tx.Exec(
				`INSERT OR REPLACE INTO kv(scope, key, value) VALUES(?, ?, ?)`, scope, k, string(b),
			); err != nil {
				return err
			}
		}
		return nil
	}

	list, ok := value.([]any)
	if !ok {
		return nil
	}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key := handlerutil.GetString(m, "providerAlias") + "|" + handlerutil.GetString(m, "id") + "|" +
			stringDefault(m["type"], "llm").(string)
		b, err := json.Marshal(m)
		if err != nil {
			continue
		}
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO kv(scope, key, value) VALUES(?, ?, ?)`, scope, key, string(b),
		); err != nil {
			return err
		}
	}
	return nil
}

// loadKVScopes fills the kv-derived export sections.
func loadKVScopes(db queryer, out *databaseExport) error {
	rows, err := db.Query(`SELECT scope, key, value FROM kv`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var scope, key, value string
		if err := rows.Scan(&scope, &key, &value); err != nil {
			return err
		}
		switch scope {
		case "modelAliases":
			out.ModelAliases[key] = parseJSONValue(value, value)
		case "mitmAlias":
			out.MitmAlias[key] = parseJSONValue(value, map[string]any{})
		case "pricing":
			out.Pricing[key] = parseJSONValue(value, map[string]any{})
		case "customModels":
			out.CustomModels = append(out.CustomModels, parseJSONValue(value, value))
		}
	}
	return rows.Err()
}

// selectRows runs a query and returns every row as a column → value map, with
// SQLite BLOB values surfaced as strings (they hold JSON columns).
func selectRows(db queryer, query string) ([]map[string]any, error) {
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	out := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for i, col := range columns {
			v := values[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			row[col] = v
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// mergeDataColumn spreads the JSON `data` blob into the row, keeping the
// explicit columns on top — the same shape Next exports per table.
func mergeDataColumn(row map[string]any, keepColumns ...string) {
	merged := map[string]any{}
	if raw, ok := row["data"].(string); ok && raw != "" {
		if parsed, ok := parseJSONValue(raw, nil).(map[string]any); ok {
			merged = parsed
		}
	}
	for _, col := range keepColumns {
		if v, ok := row[col]; ok {
			merged[col] = v
		}
	}
	for k := range row {
		delete(row, k)
	}
	for k, v := range merged {
		row[k] = v
	}
}

// restData serializes the entry minus the named columns back into the `data`
// JSON blob, mirroring Next's importDb.
func restData(m map[string]any, omit ...string) string {
	skip := make(map[string]bool, len(omit))
	for _, col := range omit {
		skip[col] = true
	}
	rest := make(map[string]any, len(m))
	for k, v := range m {
		if skip[k] {
			continue
		}
		rest[k] = v
	}
	b, err := json.Marshal(rest)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// parseJSONValue decodes a JSON string, returning fallback on failure.
func parseJSONValue(value any, fallback any) any {
	s, ok := value.(string)
	if !ok {
		return value
	}
	var out any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return fallback
	}
	return out
}

// normalizeBool converts SQLite 0/1 integers to JSON booleans on export.
func normalizeBool(row map[string]any, key string) {
	switch v := row[key].(type) {
	case int64:
		row[key] = v == 1
	case int:
		row[key] = v == 1
	case float64:
		row[key] = v == 1
	}
}

// boolToInt converts a JSON boolean backup value back to SQLite 0/1.
func boolToInt(v any) int {
	if b, ok := v.(bool); ok && !b {
		return 0
	}
	return 1
}

// stringDefault returns v when it is a non-empty string, otherwise fallback
// (nil stays nil so nullable columns round-trip).
func stringDefault(v any, fallback any) any {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}

// nowISO returns a UTC RFC3339 timestamp for rows missing one.
func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// accessShape maps a Go allowlist to the Node access object.
//
// An empty list is unrestricted and gets one deny-all sentinel row, so a
// restricted key that allows nothing cannot be mistaken for a key with no
// restriction at all.
func accessShape(models []string) map[string]any {
	if len(models) == 0 {
		return map[string]any{"restricted": false, "allow": []any{}}
	}
	if len(models) == 1 && models[0] == db.SentinelDenyAll {
		return map[string]any{"restricted": true, "allow": []any{}}
	}
	allow := make([]any, 0, len(models))
	for _, m := range models {
		allow = append(allow, m)
	}
	return map[string]any{"restricted": true, "allow": allow}
}

// importAllowlist resolves one backed-up api key to the model patterns to
// restore, following the G05 mapping rules.
//
// A Node backup carries access{restricted,allow} and a Go backup carries
// allowedModels. `access` wins when present: a restricted-empty key has an
// empty allow list, which is indistinguishable from "no restriction" if only
// the empty list is looked at — so the restricted flag has to be read, and it
// has to be read before the empty list is treated as unrestricted.
func importAllowlist(k map[string]any) []string {
	if access, ok := k["access"].(map[string]any); ok {
		restricted, _ := access["restricted"].(bool)
		allow := stringList(access["allow"])
		if !restricted {
			return nil
		}
		if len(allow) == 0 {
			return []string{db.SentinelDenyAll}
		}
		return allow
	}
	models := stringList(k["allowedModels"])
	if len(models) == 0 {
		return nil
	}
	return models
}

// stringList coerces a JSON array to a string slice, dropping empty entries.
func stringList(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// intDefault coerces a backup value to a whole non-negative count, returning
// fallback for anything else. JSON has one number type (float64), so a
// hand-edited or older backup can arrive as a float, a string, or nothing at
// all; an unparseable limit restores as its fallback (0 = unlimited) rather
// than failing the whole restore over one column.
func intDefault(v any, fallback int) int {
	switch t := v.(type) {
	case float64:
		if t >= 0 {
			return int(t)
		}
	case int:
		if t >= 0 {
			return t
		}
	case int64:
		if t >= 0 {
			return int(t)
		}
	}
	return fallback
}
