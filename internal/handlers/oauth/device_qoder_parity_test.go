package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/proxy/executor"
)

// qoderFakeTransport stands in for the Qoder device endpoints: the token poll
// plus the userinfo lookups on both the .sh and the .cn host. It lets the poll
// handler run end-to-end without touching the network.
type qoderFakeTransport struct {
	pollBody     string
	pollStatus   int
	userinfoBody string
}

func (f qoderFakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")
	path := req.URL.Path
	switch {
	case strings.Contains(path, "deviceToken/poll"):
		status := f.pollStatus
		if status == 0 {
			status = http.StatusOK
		}
		rec.WriteHeader(status)
		_, _ = rec.WriteString(f.pollBody)
	case strings.Contains(path, "userinfo"):
		rec.WriteHeader(http.StatusOK)
		_, _ = rec.WriteString(f.userinfoBody)
	default:
		rec.WriteHeader(http.StatusOK)
		_, _ = rec.WriteString(`{}`)
	}
	return rec.Result(), nil
}

func withQoderTransport(t *testing.T, f qoderFakeTransport) {
	t.Helper()
	orig := deviceClient.Transport
	deviceClient.Transport = f
	t.Cleanup(func() { deviceClient.Transport = orig })
}

func doQoderPoll(t *testing.T, repo *db.Repo, deviceCode, sessionJSON string) *httptest.ResponseRecorder {
	t.Helper()
	handler := NewOAuthHandler(repo)
	body := `{"provider":"qoder","device_code":"` + deviceCode + `","session":` + sessionJSON + `}`
	req := httptest.NewRequest("POST", "/api/oauth/device/poll", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleDevicePoll(rec, req)
	return rec
}

func qoderConnCount(t *testing.T, repo *db.Repo) int {
	t.Helper()
	var n int
	if err := repo.RawDB().QueryRow(`SELECT COUNT(*) FROM providerConnections WHERE provider='qoder'`).Scan(&n); err != nil {
		t.Fatalf("count qoder connections: %v", err)
	}
	return n
}

func qoderConnData(t *testing.T, repo *db.Repo) map[string]any {
	t.Helper()
	rows := qoderAllConnData(t, repo)
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 qoder connection, got %d", len(rows))
	}
	return rows[0]
}

func qoderAllConnData(t *testing.T, repo *db.Repo) []map[string]any {
	t.Helper()
	rs, err := repo.RawDB().Query(`SELECT data FROM providerConnections WHERE provider='qoder'`)
	if err != nil {
		t.Fatalf("query qoder connections: %v", err)
	}
	defer rs.Close()
	var out []map[string]any
	for rs.Next() {
		var dataStr string
		if err := rs.Scan(&dataStr); err != nil {
			t.Fatalf("scan qoder connection: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(dataStr), &m); err != nil {
			t.Fatalf("parse qoder connection data: %v", err)
		}
		out = append(out, m)
	}
	if err := rs.Err(); err != nil {
		t.Fatalf("iterate qoder connections: %v", err)
	}
	return out
}

// A Qoder grant that yields no user id (neither the poll body nor userinfo)
// cannot be signed or matched to an account, so the login must fail with a
// clear MISSING_UID error instead of storing an unusable credential.
func TestQoderPoll_MissingUIDFailsLogin(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	withQoderTransport(t, qoderFakeTransport{
		pollBody:     `{"token":"tok-1","refresh_token":"rt-1"}`,
		userinfoBody: `{"name":"Alice","email":"a@example.com"}`,
	})

	rec := doQoderPoll(t, repo, "dc-nouid", `{"verifier":"v","nonce":"n","machineId":"rand-A"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"status":"error"`) {
		t.Errorf("expected error status for a grant with no user id, got: %s", body)
	}
	if !strings.Contains(body, "MISSING_UID") {
		t.Errorf("expected a MISSING_UID-style error, got: %s", body)
	}
	if n := qoderConnCount(t, repo); n != 0 {
		t.Errorf("expected no connection saved without a user id, got %d", n)
	}
}

// The persisted machine id must be derived deterministically from the user id
// (the same derivation the COSY signer uses), not the fresh random UUID minted
// at device-start. Two logins for the same account must agree.
func TestQoderPoll_StableMachineIDFromUID(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	want := executor.QoderDeriveMachineID("user-abc")
	// Two logins for the same account: a re-login mints a new access token (a new
	// connection row) and carries a new random device-start machine id, but the
	// persisted machine id must stay the derived, stable one.
	for _, tc := range []struct{ deviceCode, token, sessionMachine string }{
		{"dc-A", "tok-A", "rand-A"},
		{"dc-B", "tok-B", "rand-B"},
	} {
		withQoderTransport(t, qoderFakeTransport{
			pollBody:     `{"token":"` + tc.token + `","user_id":"user-abc"}`,
			userinfoBody: `{}`,
		})
		rec := doQoderPoll(t, repo, tc.deviceCode, `{"verifier":"v","nonce":"n","machineId":"`+tc.sessionMachine+`"}`)
		if !strings.Contains(rec.Body.String(), `"status":"authorized"`) {
			t.Fatalf("login %s: expected authorized, got %s", tc.deviceCode, rec.Body.String())
		}
	}

	rows := qoderAllConnData(t, repo)
	if len(rows) != 2 {
		t.Fatalf("expected 2 qoder connections (one per login), got %d", len(rows))
	}
	for i, m := range rows {
		psd, _ := m["providerSpecificData"].(map[string]any)
		got, _ := psd["machineId"].(string)
		if got != want {
			t.Errorf("login %d: persisted machineId = %q, want derived %q", i, got, want)
		}
	}
}

// The poll's expires_in must be persisted as a real expiresAt; a grant without
// one still gets the bounded 30-day fallback rather than a null (never-expiring)
// expiry.
func TestQoderPoll_PersistsTokenExpiry(t *testing.T) {
	const thirtyDays = 30 * 24 * time.Hour
	cases := []struct {
		name     string
		pollBody string
		want     time.Duration
	}{
		{"expires_in", `{"token":"tok","user_id":"u","expires_in":86400}`, 86400 * time.Second},
		{"fallback_30d", `{"token":"tok","user_id":"u"}`, thirtyDays},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			database, cleanup := setupTestDB(t)
			defer cleanup()
			repo := db.NewRepo(database)

			withQoderTransport(t, qoderFakeTransport{pollBody: tc.pollBody, userinfoBody: `{}`})

			before := time.Now()
			rec := doQoderPoll(t, repo, "dc-exp", `{"verifier":"v"}`)
			after := time.Now()
			if !strings.Contains(rec.Body.String(), `"status":"authorized"`) {
				t.Fatalf("expected authorized, got %s", rec.Body.String())
			}

			m := qoderConnData(t, repo)
			expStr, ok := m["expiresAt"].(string)
			if !ok || expStr == "" {
				t.Fatalf("expected expiresAt to be persisted, got %v", m["expiresAt"])
			}
			exp, err := time.Parse(time.RFC3339, expStr)
			if err != nil {
				t.Fatalf("parse expiresAt %q: %v", expStr, err)
			}
			// expiresAt is persisted at second precision (RFC3339), so widen the
			// bracket by a couple of seconds to absorb the truncation.
			lo, hi := before.Add(tc.want-2*time.Second), after.Add(tc.want+2*time.Second)
			if exp.Before(lo) || exp.After(hi) {
				t.Errorf("expiresAt %v not within [%v, %v]", exp, lo, hi)
			}
		})
	}
}

// Qoder reports "still authorizing" as a 200 body carrying errorCode NotFound /
// DeviceTokenNonceRequired. Those must stay pending; any other errorCode is a
// real poll failure.
func TestQoderPoll_ErrorCodePending(t *testing.T) {
	for _, code := range []string{"NotFound", "DeviceTokenNonceRequired"} {
		t.Run(code, func(t *testing.T) {
			database, cleanup := setupTestDB(t)
			defer cleanup()
			repo := db.NewRepo(database)

			withQoderTransport(t, qoderFakeTransport{pollBody: `{"errorCode":"` + code + `"}`, userinfoBody: `{}`})

			rec := doQoderPoll(t, repo, "dc-ec", `{"verifier":"v"}`)
			body := rec.Body.String()
			if !strings.Contains(body, `"status":"pending"`) {
				t.Errorf("errorCode %s should be pending, got: %s", code, body)
			}
			if n := qoderConnCount(t, repo); n != 0 {
				t.Errorf("expected no connection saved while pending, got %d", n)
			}
		})
	}

	t.Run("other_error_code_fails", func(t *testing.T) {
		database, cleanup := setupTestDB(t)
		defer cleanup()
		repo := db.NewRepo(database)

		withQoderTransport(t, qoderFakeTransport{pollBody: `{"errorCode":"SomethingElse"}`, userinfoBody: `{}`})

		rec := doQoderPoll(t, repo, "dc-ec2", `{"verifier":"v"}`)
		body := rec.Body.String()
		if !strings.Contains(body, `"status":"error"`) {
			t.Errorf("a non-pending errorCode should fail, got: %s", body)
		}
		if n := qoderConnCount(t, repo); n != 0 {
			t.Errorf("expected no connection saved on poll failure, got %d", n)
		}
	})
}
