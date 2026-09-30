package providers

import (
	"errors"
	"fmt"
	"net/http"
)

// OAuthRefreshError carries the HTTP status a token endpoint rejected a
// refresh with. Without it a revoked grant is indistinguishable from a token
// endpoint blip once the status is flattened into a message, and the router
// keeps retrying the same dead credential on every request until the provider
// rate limits the whole egress IP.
type OAuthRefreshError struct {
	Status int
	Body   string // already truncated: a token endpoint can echo the submitted grant
}

func (e *OAuthRefreshError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("token refresh rejected with status %d", e.Status)
	}
	return fmt.Sprintf("token refresh rejected with status %d: %s", e.Status, e.Body)
}

// IsRefreshUnauthorized reports whether err is a refresh the provider
// rejected with 401, the status that means the grant itself is gone and only a
// re-login fixes it. Every other failure (5xx, transport, malformed response)
// is left to the caller's normal handling: none of them prove the credential
// is dead, and parking an account on a blip would take it out of rotation for
// no reason.
func IsRefreshUnauthorized(err error) bool {
	var refreshErr *OAuthRefreshError
	return errors.As(err, &refreshErr) && refreshErr.Status == http.StatusUnauthorized
}
