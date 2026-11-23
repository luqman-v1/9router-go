// Package kiroidentity builds the Kiro IDE identity headers shared by every
// Kiro / Amazon Q call. Kiro's client-version gate rejects older identities,
// so inference, refresh and CreateResponse must all present the same floor.
//
// The machine id is a sha256 of a stable account identifier: a rotating access
// token keeps the same id, and no raw credential reaches the wire.
package kiroidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	IDEVersion     = "1.4.154"
	SDKVersion     = "1.0.0"
	AgentOS        = "windows"
	AgentOSVersion = "10.0.26200"
	NodeVersion    = "22.21.1"
)

// Headers returns the User-Agent and x-amz-user-agent pair for client, which
// must be either "streaming" (GenerateAssistantResponse) or "runtime" (the
// other Kiro / Amazon Q APIs). Unknown clients use the runtime identity,
// matching upstream's KIRO_SDK_CLIENTS lookup default.
func Headers(seed, client string) (userAgent, amzUserAgent string) {
	api, metrics := "codewhispererruntime", "N,E"
	if client == "streaming" {
		api, metrics = "codewhispererstreaming", "E"
	}
	identity := "KiroIDE-" + IDEVersion + "-" + MachineID(seed)
	userAgent = fmt.Sprintf(
		"aws-sdk-js/%s ua/2.1 os/%s#%s lang/js md/nodejs#%s api/%s#%s m/%s %s",
		SDKVersion, AgentOS, AgentOSVersion, NodeVersion, api, SDKVersion, metrics, identity,
	)
	amzUserAgent = fmt.Sprintf("aws-sdk-js/%s %s", SDKVersion, identity)
	return userAgent, amzUserAgent
}

// MachineID hashes a seed to the 64-hex machine id used in the identity.
func MachineID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// Seed applies upstream's precedence for the stable identity seed:
// clientId > refreshToken > profileArn > apiKey > accessToken > anonymous.
// Callers pass the raw values so the choice stays in one place.
func Seed(psd map[string]any, refreshToken, accessToken string) string {
	if v, ok := psd["clientId"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if strings.TrimSpace(refreshToken) != "" {
		return strings.TrimSpace(refreshToken)
	}
	if v, ok := psd["profileArn"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, ok := psd["apiKey"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if strings.TrimSpace(accessToken) != "" {
		return strings.TrimSpace(accessToken)
	}
	return "kiro-anonymous"
}
