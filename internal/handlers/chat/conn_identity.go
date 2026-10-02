package chat

import (
	"fmt"

	json "encoding/json/v2"

	"9router/proxy/internal/models"
)

// connProjectRef mirrors the two shapes a project ID takes inside the
// providerConnections.data blob: top-level `projectId` (written by
// storeAntigravityProjectID on onboarding) and `providerSpecificData.projectId`
// (written by the dashboard when the connection is created).
type connProjectRef struct {
	ProjectID            string `json:"projectId"`
	ProviderSpecificData struct {
		ProjectID string `json:"projectId"`
	} `json:"providerSpecificData"`
}

// connIdentityKV returns the log key/value pairs identifying a connection by
// name, email and project ID. Upstream errors such as Antigravity's
// "Verify your account to continue" name neither the Google account nor the
// Cloud project, so without these fields a failure is only traceable by opening
// the dashboard. Empty fields are omitted to keep log lines readable.
func connIdentityKV(conn *models.ProviderConnection) []any {
	if conn == nil {
		return nil
	}
	kv := make([]any, 0, 6)
	if conn.Name != nil && *conn.Name != "" {
		kv = append(kv, "connName", *conn.Name)
	}
	if conn.Email != nil && *conn.Email != "" {
		kv = append(kv, "email", *conn.Email)
	}
	if pid := projectIDFromConnData(conn.Data); pid != "" {
		kv = append(kv, "projectId", pid)
	}
	return kv
}

// connIdentityKVByID looks a connection up by ID and returns its identity
// fields. Used on failure paths only, so the query never lands on the hot path.
func (h *ChatHandler) connIdentityKVByID(connectionID string) []any {
	if h.Repo == nil || connectionID == "" {
		return nil
	}
	conn, err := h.Repo.GetProviderConnectionByID(connectionID)
	if err != nil || conn == nil {
		return nil
	}
	return connIdentityKV(conn)
}

// connIdentityKVOr returns the identity fields for a request, preferring the
// name the picker already had. A query only happens when the caller could not
// supply one, so the success path — the one that runs on every request — never
// touches the database for log formatting.
func (h *ChatHandler) connIdentityKVOr(f forwardRequestParams, connectionID string) []any {
	if f.ConnName == "" && f.ConnEmail == "" {
		if kv := h.connIdentityKVByID(connectionID); len(kv) > 0 {
			return append([]any{"conn", connectionID}, kv...)
		}
	}
	kv := make([]any, 0, 6)
kv = append(kv, "conn", connectionID)
	if f.ConnName != "" {
		kv = append(kv, "connName", f.ConnName)
	}
	if f.ConnEmail != "" {
		kv = append(kv, "email", f.ConnEmail)
	}
	return kv
}

// identityNames pulls the account name and email out of a log kv slice, so a
// caller that already resolved the identity does not resolve it twice.
func identityNames(kv []any) (name, email string) {
	for i := 0; i+1 < len(kv); i += 2 {
		switch fmt.Sprint(kv[i]) {
		case "connName":
			name, _ = kv[i+1].(string)
		case "email":
			email, _ = kv[i+1].(string)
	}
	}
	return name, email
}

// connObjName returns a connection's user-facing label, or "" when it has
// none. Nil-safe so the virtual no-auth connection ("noauth"/"Public") and a
// real row read the same way.
func connObjName(conn *models.ProviderConnection) string {
	if conn == nil || conn.Name == nil {
		return ""
	}
	return *conn.Name
}

// connObjEmail returns the account email stored on a connection, or "".
func connObjEmail(conn *models.ProviderConnection) string {
	if conn == nil || conn.Email == nil {
		return ""
	}
	return *conn.Email
}

func projectIDFromConnData(data string) string {
	if data == "" {
		return ""
	}
	var ref connProjectRef
	if err := json.Unmarshal([]byte(data), &ref); err != nil {
		return ""
	}
	if ref.ProjectID != "" {
		return ref.ProjectID
	}
	return ref.ProviderSpecificData.ProjectID
}
