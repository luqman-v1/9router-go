package dashboard

// Connection test for the Zed provider.
//
// Zed cannot be probed with a plain bearer call: the stored credential is an
// account blob that only authorizes /client/* endpoints, and the model API
// needs a second, short-lived token minted from it. A real test therefore
// walks the same two layers a request does — account identity, then token
// exchange, then the catalogue — so "Test connection" fails when the account
// is revoked or the subscription lapses, instead of merely reporting that a
// token string exists.

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"9router/proxy/internal/providers"
)

// probeZed walks the Zed auth chain and reports what it found.
func (h *DashboardHandler) probeZed(ctx context.Context, data connectionProbeData, client *http.Client) probeOutcome {
	creds := providers.ZedCredentials{
		AccessToken:    data.AccessToken,
		UserID:         zedProbeString(data.ProviderSpecificData, "userId"),
		OrganizationID: zedProbeString(data.ProviderSpecificData, "organizationId"),
		SystemID:       zedProbeString(data.ProviderSpecificData, "systemId"),
	}
	if creds.AccessToken == "" {
		return probeOutcome{message: "No access token"}
	}
	if creds.UserID == "" {
		return probeOutcome{message: "Zed user id is missing — re-authorize this connection"}
	}

	// Layer 1: the account token must still identify an account.
	status, body, err := connectionProbeDo(ctx, client, http.MethodGet,
		providers.ZedCloudBaseURL()+"/client/users/me",
		map[string]string{
			"Authorization": providers.ZedAuthHeader(creds),
			"Accept":        "application/json",
		}, nil)
	if err != nil {
		return probeOutcome{message: err.Error()}
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return probeOutcome{message: "Zed token invalid or revoked — re-authorize this connection"}
	case status < 200 || status >= 300:
		return probeOutcome{message: fmt.Sprintf("Zed account API returned %d", status)}
	}

	// Layer 2: the credential must still be able to mint an LLM token, which
	// is what actually gates model access.
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	token, err := providers.ZedLLMToken(probeCtx, client, creds)
	if err != nil {
		return probeOutcome{message: "Zed rejected the token exchange: " + err.Error()}
	}
	if token == "" {
		return probeOutcome{message: "Zed returned an empty LLM token"}
	}

	// The catalogue is the last link: an active subscription lists models,
	// a lapsed one does not.
	catalog, err := providers.ZedFetchModels(probeCtx, client, creds)
	if err != nil {
		return probeOutcome{message: "Zed catalogue unavailable: " + err.Error()}
	}
	usable := 0
	for _, m := range catalog {
		if !m.IsDisabled && m.ID != "" {
			usable++
		}
	}
	if usable == 0 {
		return probeOutcome{message: "Zed returned no usable models — check the subscription"}
	}
	// Upstream only surfaces `error` on a failed test, so a success carries no
	// message; the account handle and model count are proven by the calls
	// above, not by the payload.
	_ = body
	return probeOutcome{valid: true}
}

// zedProbeString reads a string field from providerSpecificData.
func zedProbeString(psd map[string]any, key string) string {
	if psd == nil {
		return ""
	}
	value, _ := psd[key].(string)
	return value
}
