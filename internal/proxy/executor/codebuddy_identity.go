package executor

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// CodeBuddy CN anti-ban identity.
//
// Port of upstream `open-sse/shared/codebuddyIdentity.js` (PR decolua/9router
// #3257). Tencent cross-references device fingerprints sent in /v2/report
// telemetry; a proxied key that presents no stable identity looks like a
// silent/bot client and gets banned. Every value here is derived
// deterministically from the credential so that the same credential always
// yields the same identity and different credentials never share one.
//
// All functions are fail-open: they never panic, and always return a usable
// fingerprint. The raw secret is never stored, logged, or sent.

// codebuddyCLIVersion is the official CLI build this identity mimics.
// Source: @tencent-ai/codebuddy-code product.json (CLI 2.133.1).
const codebuddyCLIVersion = "2.133.1"

type codebuddyBuildInfo struct {
	ReleaseDate int64
	Commit      string
}

// codebuddyBuild mirrors the ${PWD}/pluginStatus build fields of the official
// client, kept in sync with the registry UA.
var codebuddyBuild = codebuddyBuildInfo{
	ReleaseDate: 1785400746436,
	Commit:      "e9991e2be9dcafcce0fad23fc065dd91a7f3efed",
}

// codebuddyCredential contains the material identity is anchored to. Access
// tokens rotate on refresh, so prefer the LONGEST-LIVED secret available.
type codebuddyCredential struct {
	APIKey       string // most stable
	RefreshToken string
	AccessToken  string
	ID           string // connection id
	UID          string // x-user-id for telemetry head
}

func (c codebuddyCredential) seed() string {
	switch {
	case c.APIKey != "":
		return c.APIKey
	case c.RefreshToken != "":
		return c.RefreshToken
	case c.AccessToken != "":
		return c.AccessToken
	case c.ID != "":
		return c.ID
	default:
		return "anonymous"
	}
}

func codebuddySHA256Hex(input string) string {
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])
}

func codebuddyMD5Hex(input string) string {
	h := md5.Sum([]byte(input))
	return hex.EncodeToString(h[:])
}

// hashToUUID derives a UUID-shaped string from a hex hash.
func hashToUUID(hexStr string) string {
	h := strings.ToLower(strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			return r
		}
		return -1
	}, hexStr))
	b := (h + h + h)[:32]
	return fmt.Sprintf("%s-%s-%s-%s-%s", b[0:8], b[8:12], b[12:16], b[16:20], b[20:32])
}

// lowerHex filters a string down to lowercase hex chars.
func lowerHex(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			sb.WriteRune((r - 'A' + 'a'))
		}
	}
	return sb.String()
}

// deriveQimei36 — Tencent QIMEI device fingerprint, 36 hex chars, stable per
// credential.
func deriveQimei36(c codebuddyCredential) string {
	seed := "1|qimei36|" + c.seed()
	return codebuddySHA256Hex(seed)[:36]
}

// deriveMachineID — stable machine UUID per credential.
func deriveMachineID(c codebuddyCredential) string {
	seed := "1|machineId|" + c.seed()
	return hashToUUID(codebuddySHA256Hex(seed))
}

// deriveSessionID — per-session correlation UUID, stable per credential so a
// long-running gateway behaves like one official-CLI session.
func deriveSessionID(c codebuddyCredential) string {
	seed := "1|sessionId|" + c.seed()
	return hashToUUID(codebuddySHA256Hex(seed))
}

// deriveDeviceID — MD5(hostname-platform) equivalent, stable per credential.
func deriveDeviceID(c codebuddyCredential) string {
	seed := "1|deviceId|" + c.seed()
	return codebuddyMD5Hex(seed)[:16]
}

// deriveHostID — SHA-256 host fingerprint equivalent, stable per credential.
func deriveHostID(c codebuddyCredential) string {
	seed := "1|hostId|" + c.seed()
	return codebuddySHA256Hex(seed)[:32]
}

// codebuddyVCSInfo — consistent-but-fake git identity per credential.
type codebuddyVCSInfo struct {
	VCSRepo   string `json:"vcsRepo"`
	VCSBranch string `json:"vcsBranchName"`
	VCSRevID  string `json:"vcsRevId"`
}

func deriveVCSInfo(c codebuddyCredential) codebuddyVCSInfo {
	h := codebuddySHA256Hex("1|vcs|" + c.seed())
	return codebuddyVCSInfo{
		VCSRepo:   "github.com/user/project-" + h[:6],
		VCSBranch: "main",
		VCSRevID:  h[:40],
	}
}

// codebuddyHardwareProfile — consistent-but-fake hardware profile per
// credential, so a fleet of keys does not present one perfectly uniform
// signature.
type codebuddyHardwareProfile struct {
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	OSVersion  string `json:"osVersion"`
	CPUModel   string `json:"cpuModel"`
	CPUCores   int    `json:"cpuCores"`
	MemorySize int    `json:"memorySize"`
	Timezone   string `json:"timezone"`
}

func deriveHardwareProfile(c codebuddyCredential) codebuddyHardwareProfile {
	h := codebuddyMD5Hex("1|hw|" + c.seed())
	a := int(h[0] & 0xf)
	b := int(h[1] & 0xf)
	return codebuddyHardwareProfile{
		OS:         "win32",
		Arch:       "x64",
		OSVersion:  "10.0.26200",
		CPUModel:   "13th Gen Intel(R) Core(TM) i5-13420H",
		CPUCores:   12 + a%4,      // 12-15
		MemorySize: 16 + (b%2)*16, // 16 or 32
		Timezone:   "Asia/Shanghai",
	}
}

// codebuddyIdentity is the full stable identity for one credential.
type codebuddyIdentity struct {
	Qimei36    string                   `json:"qimei36"`
	MachineID  string                   `json:"machineId"`
	SessionID  string                   `json:"sessionId"`
	DeviceID   string                   `json:"deviceId"`
	HostID     string                   `json:"hostId"`
	VCS        codebuddyVCSInfo         `json:"vcs"`
	Hardware   codebuddyHardwareProfile `json:"hardware"`
	CLIVersion string                   `json:"cliVersion"`
	Build      codebuddyBuildInfo       `json:"build"`
}

// getCodebuddyIdentity aggregates the full stable identity. Always returns a
// complete object (fail-open).
func getCodebuddyIdentity(c codebuddyCredential) codebuddyIdentity {
	// The individual derivations are all pure string ops that cannot fail; the
	// only way to panic here would be a bug, so resolve directly.
	return codebuddyIdentity{
		Qimei36:    deriveQimei36(c),
		MachineID:  deriveMachineID(c),
		SessionID:  deriveSessionID(c),
		DeviceID:   deriveDeviceID(c),
		HostID:     deriveHostID(c),
		VCS:        deriveVCSInfo(c),
		Hardware:   deriveHardwareProfile(c),
		CLIVersion: codebuddyCLIVersion,
		Build:      codebuddyBuild,
	}
}
