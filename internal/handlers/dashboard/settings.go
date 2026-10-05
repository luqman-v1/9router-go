package dashboard

import (
	"archive/zip"
	"bytes"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	json "encoding/json/v2"

	"golang.org/x/crypto/bcrypt"

	"9router/proxy/internal/auth"
	"9router/proxy/internal/config"
	"9router/proxy/internal/handlerutil"
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

	if err := h.Repo.UpdateSettingsRaw(updates); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
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

	payload, err := h.exportDatabase()
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

	if err := h.importDatabase(payload); err != nil {
		writePlainError(w, http.StatusBadRequest, err.Error())
		return
	}

	// upstream re-applies the outbound proxy env here; the Go runtime has no
	// equivalent yet (no outboundProxyUrl handling), so nothing to re-apply.

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

	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(parsed)},
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

// exportDatabase reads the dashboard tables into the shared backup payload.
func (h *DashboardHandler) exportDatabase() (*databaseExport, error) {
	db := h.Repo.RawDB()
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

	settings, err := h.Repo.GetSettingsRaw()
	if err != nil {
		return nil, err
	}
	if len(settings) > 0 {
		out.Settings = stripSecretSettings(settings)
	}

	if out.ProviderConnections, err = selectRows(db,
		`SELECT id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt FROM providerConnections`); err != nil {
		return nil, err
	}
	for _, row := range out.ProviderConnections {
		mergeDataColumn(row, "id", "provider", "authType", "name", "email", "priority", "isActive", "createdAt", "updatedAt")
		normalizeBool(row, "isActive")
	}

	if out.ProviderNodes, err = selectRows(db,
		`SELECT id, type, name, data, createdAt, updatedAt FROM providerNodes`); err != nil {
		return nil, err
	}
	for _, row := range out.ProviderNodes {
		mergeDataColumn(row, "id", "type", "name", "createdAt", "updatedAt")
	}

	if out.ProxyPools, err = selectRows(db,
		`SELECT id, isActive, testStatus, data, createdAt, updatedAt FROM proxyPools`); err != nil {
		return nil, err
	}
	for _, row := range out.ProxyPools {
		mergeDataColumn(row, "id", "isActive", "testStatus", "createdAt", "updatedAt")
		normalizeBool(row, "isActive")
	}

	if out.APIKeys, err = selectRows(db,
		`SELECT id, key, name, machineId, isActive, createdAt FROM apiKeys`); err != nil {
		return nil, err
	}
	for _, row := range out.APIKeys {
		normalizeBool(row, "isActive")
	}

	if out.Combos, err = selectRows(db,
		`SELECT id, name, kind, models, createdAt, updatedAt FROM combos`); err != nil {
		return nil, err
	}
	for _, row := range out.Combos {
		row["models"] = parseJSONValue(row["models"], []any{})
	}

	if err := loadKVScopes(db, out); err != nil {
		return nil, err
	}
	return out, nil
}

// importDatabase wipes the dashboard tables and restores a backup payload,
// matching Next's importDb transaction.
func (h *DashboardHandler) importDatabase(payload map[string]any) error {
	db := h.Repo.RawDB()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Secrets are stripped from the backup, so a restore must not let their
	// absence delete the live credentials: wiping the settings row without
	// them would silently reset the dashboard to the default password and
	// disable OIDC. Read them inside the transaction before the wipe.
	liveSecrets, err := readSettingsSecrets(tx)
	if err != nil {
		return err
	}

	wipes := []string{
		`DELETE FROM settings`,
		`DELETE FROM providerConnections`,
		`DELETE FROM providerNodes`,
		`DELETE FROM proxyPools`,
		`DELETE FROM apiKeys`,
		`DELETE FROM combos`,
		`DELETE FROM kv WHERE scope IN ('modelAliases', 'customModels', 'mitmAlias', 'pricing')`,
	}
	for _, stmt := range wipes {
		if _, err := tx.Exec(stmt); err != nil {
			return err
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
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO settings(id, data) VALUES(1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, string(b),
		); err != nil {
			return err
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
			return err
		}
	}

	nodes, _ := payload["providerNodes"].([]any)
	for _, item := range nodes {
		n, ok := item.(map[string]any)
		if !ok {
			continue
		}
		data := restData(n, "id", "type", "name", "createdAt", "updatedAt")
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO providerNodes(id, type, name, data, createdAt, updatedAt) VALUES(?, ?, ?, ?, ?, ?)`,
			n["id"], stringDefault(n["type"], nil), stringDefault(n["name"], nil), data,
			stringDefault(n["createdAt"], nowISO()), stringDefault(n["updatedAt"], nowISO()),
		); err != nil {
			return err
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
			return err
		}
	}

	keys, _ := payload["apiKeys"].([]any)
	for _, item := range keys {
		k, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO apiKeys(id, key, name, machineId, isActive, createdAt) VALUES(?, ?, ?, ?, ?, ?)`,
			k["id"], k["key"], stringDefault(k["name"], nil), stringDefault(k["machineId"], nil),
			boolToInt(k["isActive"]), stringDefault(k["createdAt"], nowISO()),
		); err != nil {
			return err
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
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO combos(id, name, kind, models, createdAt, updatedAt) VALUES(?, ?, ?, ?, ?, ?)`,
			c["id"], c["name"], stringDefault(c["kind"], nil), string(models),
			stringDefault(c["createdAt"], nowISO()), stringDefault(c["updatedAt"], nowISO()),
		); err != nil {
			return err
		}
	}

	if err := writeKVPayload(tx, "modelAliases", payload["modelAliases"], true); err != nil {
		return err
	}
	if err := writeKVPayload(tx, "customModels", payload["customModels"], false); err != nil {
		return err
	}
	if err := writeKVPayload(tx, "mitmAlias", payload["mitmAlias"], true); err != nil {
		return err
	}
	if err := writeKVPayload(tx, "pricing", payload["pricing"], true); err != nil {
		return err
	}

	return tx.Commit()
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
func loadKVScopes(db *sql.DB, out *databaseExport) error {
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
func selectRows(db *sql.DB, query string) ([]map[string]any, error) {
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
