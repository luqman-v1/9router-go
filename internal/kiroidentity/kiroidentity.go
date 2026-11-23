// Package kiroidentity builds the Kiro IDE identity headers shared by the
// inference proxy and the social-token refresher. It is a leaf package so both
// callers can import it without an import cycle.
//
// Ported from open-sse/shared/kiroIdentity.js (decolua/9router #1da78e2bf).
package kiroidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// ClientKind selects the AWS API surface the identity is minted for.
// Upstream uses codewhispererstreaming for inference and codewhispererruntime
// for the social refresh endpoint.
type ClientKind string

const (
	ClientKindStreaming ClientKind = "streaming"
	ClientKindRuntime   ClientKind = "runtime"
)

// Identity values mirror the seed object upstream buildKiroIdentityHeaders
// receives. Every field is optional; empty values are skipped in priority
// order when deriving the machine id.
type Identity struct {
	ClientID     string // providerSpecificData.clientId
	RefreshToken string
	ProfileARN   string // providerSpecificData.profileArn
	APIKey       string
	AccessToken  string
}

const (
	// SDKVersion is KIRO_SDK_VERSION: the aws-sdk-js version and the api
	// version interpolated into the User-Agent.
	SDKVersion     = "1.0.0"
	IDEVersion     = "1.4.154"
	AgentOS        = "windows"
	AgentOSVersion = "10.0.26200"
	NodeVersion    = "22.21.1"
	anonymousSeed  = "kiro-anonymous"
)

// MachineID returns the sha256 hex of the first non-empty seed in upstream
// priority order: clientId, refreshToken, profileArn, apiKey, accessToken.
// An all-empty identity falls back to "kiro-anonymous" so a machine id is
// always present and stable.
func MachineID(id Identity) string {
	seed := firstNonEmpty(
		id.ClientID,
		id.RefreshToken,
		id.ProfileARN,
		id.APIKey,
		id.AccessToken,
	)
	if seed == "" {
		seed = anonymousSeed
	}
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// Headers returns the exact User-Agent and X-Amz-User-Agent strings upstream
// sends for the requested client kind.
func Headers(kind ClientKind, id Identity) (userAgent, amzUserAgent string) {
	api, metrics := "codewhispererstreaming", "E"
	if kind == ClientKindRuntime {
		api, metrics = "codewhispererruntime", "N,E"
	}
	machineID := MachineID(id)
	userAgent = fmt.Sprintf(
		"aws-sdk-js/%s ua/2.1 os/%s#%s lang/js md/nodejs#%s api/%s#%s m/%s KiroIDE-%s-%s",
		SDKVersion, AgentOS, AgentOSVersion, NodeVersion, api, SDKVersion, metrics, IDEVersion, machineID,
	)
	amzUserAgent = fmt.Sprintf("aws-sdk-js/%s KiroIDE-%s-%s", SDKVersion, IDEVersion, machineID)
	return userAgent, amzUserAgent
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
