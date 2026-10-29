package executor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"9router/proxy/internal/browserlogin"
	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

// The renewal writes the credential it produced back to the row.
//
// Without that write the renewal would only produce a credential for the
// operator to paste by hand, which is the thing the renewal exists to
// remove.
func TestDeepSeekRenewalStoresTheNewCredential(t *testing.T) {
	repo, conn := deepSeekRenewalFixture(t)
	deepSeekRenewalSetData(t, repo, conn, `{"apiKey":"the-old-token","providerSpecificData":{"deepSeekCookie":"ds_session_id=old"}}`)

	restoreOpen(t, func(context.Context) (*browserlogin.DeepSeekResult, error) {
		return &browserlogin.DeepSeekResult{
			UserToken:    "the-new-token",
			CookieHeader: "ds_session_id=new",
		}, nil
	})

	outcome, err := RenewDeepSeekCredential(context.Background(), repo, conn.ID)
	if err != nil {
		t.Fatalf("RenewDeepSeekCredential: %v", err)
	}
	if !outcome.Renewed {
		t.Error("the renewal reported nothing renewed")
	}
	if outcome.UserToken != "the-new-token" {
		t.Errorf("userToken = %q, want %q", outcome.UserToken, "the-new-token")
	}

	stored := deepSeekRenewalStoredData(t, repo, conn.ID)
	var raw map[string]any
	if err := json.Unmarshal([]byte(stored), &raw); err != nil {
		t.Fatalf("the stored payload is not JSON: %v", err)
	}
	if raw["apiKey"] != "the-new-token" {
		t.Errorf("stored apiKey = %v, want the new token", raw["apiKey"])
	}
	providerData, _ := raw["providerSpecificData"].(map[string]any)
	if providerData["deepSeekCookie"] != "ds_session_id=new" {
		t.Errorf("stored cookie = %v, want the new cookie", providerData["deepSeekCookie"])
	}
}

// The renewal leaves the rest of the row alone. A full-row write would
// revert a rename or a priority change the operator made while the browser
// was open.
func TestDeepSeekRenewalLeavesTheRestOfTheRowAlone(t *testing.T) {
	repo, conn := deepSeekRenewalFixture(t)
	deepSeekRenewalSetData(t, repo, conn, `{"apiKey":"old","name":"kept","priority":7,"providerSpecificData":{"deepSeekCookie":"c","other":"kept-too"}}`)

	restoreOpen(t, func(context.Context) (*browserlogin.DeepSeekResult, error) {
		return &browserlogin.DeepSeekResult{UserToken: "new", CookieHeader: "c2"}, nil
	})

	if _, err := RenewDeepSeekCredential(context.Background(), repo, conn.ID); err != nil {
		t.Fatalf("RenewDeepSeekCredential: %v", err)
	}

	stored := deepSeekRenewalStoredData(t, repo, conn.ID)
	var raw map[string]any
	if err := json.Unmarshal([]byte(stored), &raw); err != nil {
		t.Fatalf("the stored payload is not JSON: %v", err)
	}
	if raw["name"] != "kept" {
		t.Errorf("name = %v, want it kept", raw["name"])
	}
	if raw["priority"] != float64(7) {
		t.Errorf("priority = %v, want it kept", raw["priority"])
	}
	providerData, _ := raw["providerSpecificData"].(map[string]any)
	if providerData["other"] != "kept-too" {
		t.Errorf("the other provider data = %v, want it kept", providerData["other"])
	}
}

// A renewal that produced no token stored nothing: an empty credential
// would replace a working one with a broken one.
func TestDeepSeekRenewalStoresNothingWithoutAToken(t *testing.T) {
	repo, conn := deepSeekRenewalFixture(t)
	deepSeekRenewalSetData(t, repo, conn, `{"apiKey":"the-old-token"}`)

	restoreOpen(t, func(context.Context) (*browserlogin.DeepSeekResult, error) {
		return &browserlogin.DeepSeekResult{CookieHeader: "ds_session_id=new"}, nil
	})

	if _, err := RenewDeepSeekCredential(context.Background(), repo, conn.ID); err == nil {
		t.Fatal("the renewal reported success with no token")
	}
	if stored := deepSeekRenewalStoredData(t, repo, conn.ID); stored != `{"apiKey":"the-old-token"}` {
		t.Errorf("stored = %q, want the old credential untouched", stored)
	}
}

// A renewal of another provider's connection is refused: the browser would
// sign in to DeepSeek and the result would be written to the wrong row.
func TestDeepSeekRenewalRefusesAnotherProvider(t *testing.T) {
	repo, conn := deepSeekRenewalFixture(t)
	conn.Provider = "gemini-web"

	restoreOpen(t, func(context.Context) (*browserlogin.DeepSeekResult, error) {
		return &browserlogin.DeepSeekResult{UserToken: "t"}, nil
	})

	if _, err := RenewDeepSeekCredential(context.Background(), repo, conn.ID); err == nil {
		t.Fatal("the renewal accepted a connection of another provider")
	}
}

// A browser that refuses is reported as a failure and nothing is written.
func TestDeepSeekRenewalReportsAFailedBrowser(t *testing.T) {
	repo, conn := deepSeekRenewalFixture(t)
	deepSeekRenewalSetData(t, repo, conn, `{"apiKey":"the-old-token"}`)

	restoreOpen(t, func(context.Context) (*browserlogin.DeepSeekResult, error) {
		return nil, errors.New("the browser window was closed before the sign-in completed")
	})

	if _, err := RenewDeepSeekCredential(context.Background(), repo, conn.ID); err == nil {
		t.Fatal("the renewal reported success with a failed browser")
	}
	if stored := deepSeekRenewalStoredData(t, repo, conn.ID); stored != `{"apiKey":"the-old-token"}` {
		t.Errorf("stored = %q, want the old credential untouched", stored)
	}
}

// A renewal clears the health state that parked the account under the OLD
// credential; otherwise the freshly renewed row would stay out of rotation
// for the rest of the old cooldown.
func TestDeepSeekRenewalClearsStaleHealth(t *testing.T) {
	repo, conn := deepSeekRenewalFixture(t)
	future := time.Now().UTC().Add(72 * time.Hour).Format(time.RFC3339)
	deepSeekRenewalSetData(t, repo, conn, `{"apiKey":"old","rateLimitedUntil":"`+future+`","modelLock_deepseek-v4-flash-think":"`+future+`","providerSpecificData":{"deepSeekCookie":"c","other":"kept"}}`)

	restoreOpen(t, func(context.Context) (*browserlogin.DeepSeekResult, error) {
		return &browserlogin.DeepSeekResult{UserToken: "new", CookieHeader: "c2"}, nil
	})

	if _, err := RenewDeepSeekCredential(context.Background(), repo, conn.ID); err != nil {
		t.Fatalf("RenewDeepSeekCredential: %v", err)
	}

	stored := deepSeekRenewalStoredData(t, repo, conn.ID)
	var raw map[string]any
	if err := json.Unmarshal([]byte(stored), &raw); err != nil {
		t.Fatalf("the stored payload is not JSON: %v", err)
	}
	if raw["apiKey"] != "new" {
		t.Errorf("stored apiKey = %v, want the new token", raw["apiKey"])
	}
	if raw["rateLimitedUntil"] != nil {
		t.Errorf("rateLimitedUntil = %v, want it cleared", raw["rateLimitedUntil"])
	}
	if raw["modelLock_deepseek-v4-flash-think"] != nil {
		t.Errorf("model lock = %v, want it cleared", raw["modelLock_deepseek-v4-flash-think"])
	}
	providerData, _ := raw["providerSpecificData"].(map[string]any)
	if providerData["other"] != "kept" {
		t.Errorf("the other provider data = %v, want it kept", providerData["other"])
	}
}

// deepSeekRenewalFixture is a repository and a connection to renew.
//
// The repository is a real one over a temporary database, so the connection
// lookup and the data write are the code that runs in production. Only the
// browser is stood in for.
func deepSeekRenewalFixture(t *testing.T) (*db.Repo, *models.ProviderConnection) {
	t.Helper()
	sqldb, err := db.OpenDatabase(t.TempDir() + "/renewal.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	if err := db.EnsureCoreSchema(sqldb); err != nil {
		t.Fatal(err)
	}
	repo := db.NewRepo(sqldb)
	conn := &models.ProviderConnection{ID: "conn-1", Provider: "deepseek-web"}
	if err := repo.CreateProviderConnectionFull(conn.ID, conn.Provider, "cookie", "renewal-test", nil, "{}"); err != nil {
		t.Fatal(err)
	}

	// The stubbed lookup hands back this connection, and the row in the
	// database is what the read-back sees — so the two have to agree.
	restoreConn(t, repo, conn)
	return repo, conn
}

// deepSeekRenewalSetData writes a connection's data column, so the row and
// the stubbed lookup agree.
func deepSeekRenewalSetData(t *testing.T, repo *db.Repo, conn *models.ProviderConnection, data string) {
	t.Helper()
	conn.Data = data
	if err := repo.UpdateConnectionData(conn.ID, data); err != nil {
		t.Fatalf("write the connection's data: %v", err)
	}
}

// restoreConn stands the connection lookup in for one connection.
func restoreConn(t *testing.T, repo *db.Repo, conn *models.ProviderConnection) {
	t.Helper()
	original := deepSeekRenewalConn
	deepSeekRenewalConn = func(_ *db.Repo, id string) (*models.ProviderConnection, error) {
		if id != conn.ID {
			return nil, errors.New("no such connection")
		}
		return conn, nil
	}
	t.Cleanup(func() { deepSeekRenewalConn = original })
}

// restoreOpen stands the browser in for the renewal.
func restoreOpen(t *testing.T, open func(context.Context) (*browserlogin.DeepSeekResult, error)) {
	t.Helper()
	original := deepSeekRenewalOpen
	deepSeekRenewalOpen = open
	t.Cleanup(func() { deepSeekRenewalOpen = original })
}

// deepSeekRenewalStoredData reads the data column back off the row.
func deepSeekRenewalStoredData(t *testing.T, repo *db.Repo, id string) string {
	t.Helper()
	conn, err := repo.GetProviderConnectionByID(id)
	if err != nil {
		t.Fatalf("read the connection back: %v", err)
	}
	if conn == nil {
		t.Fatal("the connection is gone")
	}
	return conn.Data
}
