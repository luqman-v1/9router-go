package middleware

import (
	"9router/proxy/internal/log"
	"context"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
	"9router/proxy/internal/keikey"
)

// ContextKey is a custom type for context keys to avoid collisions.
type ContextKey string

// ApiKeyContextKey is the context key for the authenticated API key object.
const ApiKeyContextKey ContextKey = "apiKey"

// RequireApiKey creates a middleware handler that authenticates requests using client API keys.
// It checks the Authorization header (Bearer <key>) and the query parameter `key`.
// Valid keys are retrieved from the SQLite database; inactive or disabled keys are rejected with 401.
func RequireApiKey(repo *db.Repo) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKeyString := ExtractApiKey(r)
			if apiKeyString == "" {
				handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Authentication required. Provide an API key via Authorization: Bearer <key> header or ?key=<key> query parameter.")
				return
			}

			// Validate via SQLite repository and retrieve details
			lookup := keikey.LookupHash(apiKeyString)
			apiKeyObj, err := resolveApiKey(repo, lookup, apiKeyString)
			if err != nil {
				log.Error("auth", "DB lookup error", "error", err)
				handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Internal server error")
				return
			}
			if apiKeyObj == nil {
				handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Invalid API key.")
				return
			}

			if !verifyApiKey(apiKeyString, apiKeyObj) {
				handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Invalid API key.")
				return
			}

			// Only a verified key enters the cache, and only for 5s: a
			// revoked or deleted key cannot outlive that window.
			if lookup != "" {
				keikey.DefaultAuthCache().Set(lookup, apiKeyObj)
			}

			if apiKeyObj.IsActive != 1 {
				handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Invalid or inactive API key.")
				return
			}

			// F-14: Check key expiry
			if apiKeyObj.ExpiresAt != nil && *apiKeyObj.ExpiresAt != "" {
				expires, err := time.Parse(time.RFC3339, *apiKeyObj.ExpiresAt)
				if err == nil && time.Now().After(expires) {
					handlerutil.WriteJSONError(w, http.StatusUnauthorized, "API key expired.")
					return
				}
			}

			// F-14 usage accounting. Best-effort: the row write must never turn
			// a served request into a failure, and a failed write only means the
			// resale bookkeeping undercounts by one.
			//
			// It runs after every check that can reject, so a request refused for
			// a bad key, a disabled key, or an expired contract is never counted
			// as usage.
			if apiKeyObj.ID != "" {
				if uErr := repo.UpdateApiKeyUsage(apiKeyObj.ID); uErr != nil {
					log.Warn("auth", "api key usage update failed", "key", apiKeyObj.ID, "error", uErr)
				}
			}

			// Inject API Key info into the request context for downstream handlers/logging
			ctx := context.WithValue(r.Context(), ApiKeyContextKey, apiKeyObj)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// resolveApiKey finds the row for an incoming key in cost order: the argon2
// verification cache, then the hashed lookup, then the legacy plaintext
// column. A legacy hit self-heals by hashing the row in place, so an existing
// plaintext key keeps authenticating across the upgrade.
func resolveApiKey(repo *db.Repo, lookup, plaintext string) (*models.APIKey, error) {
	if cached, ok := keikey.DefaultAuthCache().Get(lookup); ok {
		return cached.(*models.APIKey), nil
	}
	apiKey, err := repo.FindApiKeyByLookup(lookup)
	if err != nil {
		return nil, err
	}
	if apiKey != nil {
		return apiKey, nil
	}
	apiKey, err = repo.GetApiKeyByKey(plaintext)
	if err != nil || apiKey == nil {
		return nil, err
	}
	hash, err := keikey.Hash(plaintext)
	if err != nil {
		log.Warn("auth", "api key hashing failed; keeping legacy row", "error", err)
		return apiKey, nil
	}
	if err := repo.HashApiKeyRow(apiKey.ID, hash, lookup, keikey.Mask(plaintext)); err != nil {
		log.Warn("auth", "api key row self-heal failed; key still valid", "error", err)
		return apiKey, nil
	}
	apiKey.Key = ""
	return apiKey, nil
}

// verifyApiKey checks the secret against the row. Hashed rows are verified
// with argon2id; a legacy row that still has no verifier matches on lookup
// alone (the plaintext comparison already happened in the WHERE clause).
func verifyApiKey(plaintext string, apiKey *models.APIKey) bool {
	if apiKey.KeyHash == nil || *apiKey.KeyHash == "" {
		return true
	}
	return keikey.Verify(plaintext, *apiKey.KeyHash)
}

// GetAuthenticatedApiKey retrieves the authenticated APIKey object from the request context.
func GetAuthenticatedApiKey(r *http.Request) *models.APIKey {
	val := r.Context().Value(ApiKeyContextKey)
	if val == nil {
		return nil
	}
	keyObj, ok := val.(*models.APIKey)
	if !ok {
		return nil
	}
	return keyObj
}

// ExtractApiKey extracts the client API key from the request.
// For standard API routes, only header-based auth is accepted to avoid leaks.
// For EventSource / SSE stream endpoints (where browsers cannot set custom headers),
// query parameters `key` or `apiKey` are accepted as a fallback.
func ExtractApiKey(r *http.Request) string {
	// 1. Try Authorization header
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
			return strings.TrimSpace(parts[1])
		}
	}

	// 2. Try custom X-API-Key header as fallback
	if xApiKey := r.Header.Get("X-API-Key"); xApiKey != "" {
		return xApiKey
	}

	// 3. For EventSource / SSE endpoints, accept query parameter key or apiKey
	if strings.HasSuffix(r.URL.Path, "/stream") {
		if qKey := r.URL.Query().Get("key"); qKey != "" {
			return qKey
		}
		if qKey := r.URL.Query().Get("apiKey"); qKey != "" {
			return qKey
		}
	}

	return ""
}
