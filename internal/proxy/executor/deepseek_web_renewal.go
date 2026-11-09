package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"time"

	"9router/proxy/internal/browserlogin"
	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
)

// The renewal of a DeepSeek Web credential.
//
// This provider's credential cannot be renewed by a client, and that is a
// measured fact rather than a guess. The site's API authenticates with a
// `userToken` that only a real sign-in issues — there is no refresh token
// to exchange — and the WAF's token is renewed by the browser running the
// WAF's JavaScript challenge. A plain request that presents a valid cookie
// is answered with HTTP 200 and NO `Set-Cookie` at all: the site does not
// re-issue the WAF token to a client that did not solve the challenge.
//
// So a renewal has two halves and both of them need a browser:
//
//   - the `userToken`, which a sign-in mints; and
//   - the site's cookies, which the WAF's challenge renews.
//
// The renewal therefore drives a browser: it opens a throwaway profile,
// waits for the sign-in, and reads both halves out of it. The operator is
// in the loop because a sign-in needs a human.
//
// The credential is persisted back to the row, so a renewal is a one-click
// operation rather than a paste.

// ErrDeepSeekRenewalUnavailable means the renewal cannot run on this
// machine.
var ErrDeepSeekRenewalUnavailable = errors.New("this machine cannot run the DeepSeek renewal")

// deepSeekRenewalTimeout bounds the whole renewal.
//
// A sign-in that takes longer than this is a sign-in that is not going to
// happen, and the operator needs the connection back rather than a request
// that never returns.
const deepSeekRenewalTimeout = 4 * time.Minute

// deepSeekRenewalOpen is the flow that opens the browser and reads the
// credential out of it.
//
// It is a variable so a test can stand in for the browser, the way the other
// outbound calls in this package are.
var deepSeekRenewalOpen = browserlogin.RunDeepSeek

// deepSeekRenewalConn is the connection the renewal reads.
//
// It is a variable so a test can drive the renewal without a database.
var deepSeekRenewalConn = func(repo *db.Repo, id string) (*models.ProviderConnection, error) {
	return repo.GetProviderConnectionByID(id)
}

// DeepSeekRenewalOutcome is what one renewal produced.
type DeepSeekRenewalOutcome struct {
	// Renewed reports whether a new credential was stored.
	Renewed bool

	// UserToken is the credential's API half, when the renewal produced one.
	UserToken string

	// Cookie is the credential's cookie half, when the renewal produced one.
	Cookie string
}

// RenewDeepSeekCredential renews one DeepSeek Web connection's credential
// and stores the result on the row.
//
// The browser is opened with a disposable profile and the profile is deleted
// on the way out, so nothing is left running that could rotate the session
// afterwards — the same reasoning as the Gemini Web flow.
//
// The stored credential is the site's own: the `userToken` the API
// authenticates with and the cookies the WAF reads. The cookies are sealed
// with the rest of the credential by the repository.
func RenewDeepSeekCredential(ctx context.Context, repo *db.Repo, connectionID string) (*DeepSeekRenewalOutcome, error) {
	if repo == nil {
		return nil, errors.New("renew the DeepSeek credential: no database")
	}
	conn, err := deepSeekRenewalConn(repo, connectionID)
	if err != nil {
		return nil, fmt.Errorf("renew the DeepSeek credential: read the connection: %w", err)
	}
	if conn == nil {
		return nil, fmt.Errorf("renew the DeepSeek credential: no connection %q", connectionID)
	}
	if conn.Provider != "deepseek-web" {
		return nil, fmt.Errorf("renew the DeepSeek credential: %q is a %q connection", connectionID, conn.Provider)
	}

	renewCtx, cancel := context.WithTimeout(ctx, deepSeekRenewalTimeout)
	defer cancel()

	result, err := deepSeekRenewalOpen(renewCtx)
	if err != nil {
		return nil, fmt.Errorf("renew the DeepSeek credential: %w", err)
	}
	if result == nil || result.UserToken == "" {
		return nil, errors.New("renew the DeepSeek credential: the browser produced no userToken")
	}

	if err := deepSeekStoreCredential(ctx, repo, conn, result); err != nil {
		return nil, err
	}
	log.Info("deepseek-web", "renewed the credential", "conn", connectionID)
	return &DeepSeekRenewalOutcome{
		Renewed:   true,
		UserToken: result.UserToken,
		Cookie:    result.CookieHeader,
	}, nil
}

// deepSeekStoreCredential writes a renewed credential back to the row.
//
// The row's data is read, the two credential fields are replaced, and the
// whole thing is written back. Only the data column is written: a full-row
// update would write back the name, priority and active flag the row had
// when it was read, which reverts a change the operator just made.
func deepSeekStoreCredential(ctx context.Context, repo *db.Repo, conn *models.ProviderConnection, result *browserlogin.DeepSeekResult) error {
	raw := map[string]any{}
	if conn.Data != "" {
		if err := json.Unmarshal([]byte(conn.Data), &raw); err != nil {
			return fmt.Errorf("renew the DeepSeek credential: read the connection's data: %w", err)
		}
	}
	if raw == nil {
		raw = map[string]any{}
	}

	raw["apiKey"] = result.UserToken
	providerData, _ := raw["providerSpecificData"].(map[string]any)
	if providerData == nil {
		providerData = map[string]any{}
	}
	if result.CookieHeader != "" {
		providerData["deepSeekCookie"] = result.CookieHeader
	}
	raw["providerSpecificData"] = providerData

	encoded, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("renew the DeepSeek credential: encode the new credential: %w", err)
	}
	if err := repo.UpdateConnectionData(conn.ID, string(encoded)); err != nil {
		return fmt.Errorf("renew the DeepSeek credential: store the new credential: %w", err)
	}
	// The old credential was what got the account parked; a renewal that left
	// the park in place would keep the freshly authenticated row out of
	// rotation for the rest of the old cooldown.
	if err := repo.ClearConnectionRateLimit(conn.ID); err != nil {
		return fmt.Errorf("renew the DeepSeek credential: clear the old cooldown: %w", err)
	}
	if err := repo.ClearConnectionModelLocks(ctx, conn.ID); err != nil {
		return fmt.Errorf("renew the DeepSeek credential: clear the old model locks: %w", err)
	}
	return nil
}
