package providers

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"regexp"
	"strings"
	"sync"
)

// GitHub Copilot request identity.
//
// Ported from OmniRoute's open-sse/config/providerHeaderProfiles.ts, which in
// turn captured the identity the real `@github/copilot` CLI sends. The key
// insight is that Copilot exposes a WIDER entitled model catalogue to the CLI
// identity (`copilot-integration-id: copilot-developer-cli`) than to the VS Code
// extension identity (`vscode-chat`). The old narrow headers made model import
// surface fewer models than the account actually owns, and advertise ones it
// was not entitled to — which then fail upstream with `400 ... not supported`.
//
// Constants and their env overrides mirror the reference so a future upstream
// version bump can be applied without touching call sites.
const (
	// GitHubCopilotAPIVersion is the x-github-api-version value.
	GitHubCopilotAPIVersion = "2026-08-01"

	// GitHubCopilotCLIVersion is the captured CLI version. Overridable through
	// GITHUB_COPILOT_CLI_VERSION so operators can follow a newer CLI without a
	// rebuild (and tests can pin it).
	//
	// The value is load-bearing: the inference host answers any older CLI with
	// HTTP 466 {"code":"client_upgrade_required"} ("This version of the Copilot
	// CLI is no longer supported"). 1.0.81-6 tripped that gate on 2026-10-27.
	GitHubCopilotCLIVersion = "1.0.104"

	githubCopilotVersionEnv = "GITHUB_COPILOT_CLI_VERSION"
	githubCopilotMachineEnv = "GITHUB_COPILOT_MACHINE_ID"

	// GitHubCopilotRefreshUserAgent is the (older) UA the token-refresh endpoint
	// expects. The reference hard-codes it here rather than the CLI version —
	// the refresh host rejects the newer string.
	GitHubCopilotRefreshUserAgent = "GithubCopilot/1.0"
)

// safeCopilotVersionPattern bounds the env override to a version-shaped token
// so a malformed value cannot inject header syntax.
var safeCopilotVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// GitHubCopilotCLIVersionString returns the effective CLI version, honouring a
// safe GITHUB_COPILOT_CLI_VERSION override.
func GitHubCopilotCLIVersionString() string {
	if raw, ok := os.LookupEnv(githubCopilotVersionEnv); ok {
		if v := strings.TrimSpace(raw); v != "" && safeCopilotVersionPattern.MatchString(v) {
			return v
		}
	}
	return GitHubCopilotCLIVersion
}

// GitHubCopilotChatUserAgent is the request-time Copilot Chat UA
// ("GitHubCopilotChat/<version>").
func GitHubCopilotChatUserAgent() string {
	return "GitHubCopilotChat/" + GitHubCopilotCLIVersionString()
}

// GitHubCopilotCLIUserAgent is the inference-path UA the CLI sends
// ("copilot/<version>").
func GitHubCopilotCLIUserAgent() string {
	return "copilot/" + GitHubCopilotCLIVersionString()
}

// GitHubCopilotEditorVersion is the editor-version header value
// ("copilot/<version>").
func GitHubCopilotEditorVersion() string {
	return "copilot/" + GitHubCopilotCLIVersionString()
}

var (
	githubCopilotMachineOnce sync.Once
	githubCopilotMachineID   string
)

// GitHubCopilotMachineID returns a stable per-process device fingerprint for the
// x-client-machine-id header. The real CLI sends ONE stable UUID on every
// inference and /models call (verified identical across captures); minting a
// fresh random id per request would itself be an anti-fingerprint tell. The
// value is cached for the process lifetime and overridable via
// GITHUB_COPILOT_MACHINE_ID.
func GitHubCopilotMachineID() string {
	if raw, ok := os.LookupEnv(githubCopilotMachineEnv); ok {
		if v := strings.TrimSpace(raw); v != "" {
			return v
		}
	}
	githubCopilotMachineOnce.Do(func() {
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			githubCopilotMachineID = "00000000-0000-4000-8000-000000000000"
			return
		}
		// Shape it as a UUIDv4 for verisimilitude with crypto.randomUUID().
		buf[6] = (buf[6] & 0x0f) | 0x40
		buf[8] = (buf[8] & 0x3f) | 0x80
		hexed := hex.EncodeToString(buf)
		githubCopilotMachineID = hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] +
			"-" + hexed[16:20] + "-" + hexed[20:32]
	})
	return githubCopilotMachineID
}

// GitHubCopilotChatHeaders builds the inference-path header set.
//
// It deliberately sends EXACTLY the CLI's header set. An incomplete OR
// over-complete fingerprint is itself a flagging signal, so the VS Code-only
// headers (`editor-plugin-version`, `x-vscode-user-agent-library-version`) are
// intentionally absent.
//
// accept and initiator let callers vary the streamed/JSON accept header and the
// user/agent initiator; empty values fall back to the CLI defaults.
func GitHubCopilotChatHeaders(accept, initiator string) map[string]string {
	if strings.TrimSpace(accept) == "" {
		accept = "application/json"
	}
	if strings.TrimSpace(initiator) == "" {
		initiator = "user"
	}
	version := GitHubCopilotCLIVersionString()
	return map[string]string{
		"copilot-integration-id": "copilot-developer-cli",
		"editor-version":         "copilot/" + version,
		"user-agent":             "copilot/" + version,
		"openai-intent":          "conversation-agent",
		"x-interaction-type":     "conversation-user",
		"copilot-harness-id":     "copilot-sdk",
		"x-github-api-version":   GitHubCopilotAPIVersion,
		"x-client-machine-id":    GitHubCopilotMachineID(),
		"X-Initiator":            initiator,
		"Accept":                 accept,
		"Content-Type":           "application/json",
	}
}

// GitHubCopilotRefreshHeaders builds the header set for the
// copilot_internal/v2/token refresh call.
//
// authorization must already include the scheme GitHub expects — note it is
// `token <github_access_token>`, NOT `Bearer ...`. That asymmetry is a quirk of
// the internal endpoint and is why it cannot use the generic OAuth refresher.
func GitHubCopilotRefreshHeaders(authorization string) map[string]string {
	version := GitHubCopilotCLIVersionString()
	return map[string]string{
		"Authorization":         authorization,
		"Accept":                "application/json",
		"User-Agent":            GitHubCopilotRefreshUserAgent,
		"Editor-Version":        "copilot/" + version,
		"Editor-Plugin-Version": "copilot/" + version,
	}
}
